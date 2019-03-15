/*
Copyright 2018 The Kubernetes Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/ioutil"
	"net/http"
	"net/url"
	"strings"
	"time"

	prowjobv1 "k8s.io/test-infra/prow/apis/prowjobs/v1"
	prowjobset "k8s.io/test-infra/prow/client/clientset/versioned"
	prowjobscheme "k8s.io/test-infra/prow/client/clientset/versioned/scheme"
	prowjobinfov1 "k8s.io/test-infra/prow/client/informers/externalversions/prowjobs/v1"
	prowjoblisters "k8s.io/test-infra/prow/client/listers/prowjobs/v1"
	"k8s.io/test-infra/prow/kube"
	"k8s.io/test-infra/prow/pjutil"
	"k8s.io/test-infra/prow/pod-utils/decorate"
	"k8s.io/test-infra/prow/pod-utils/downwardapi"

	pipelinev1alpha1 "github.com/knative/build-pipeline/pkg/apis/pipeline/v1alpha1"
	duckv1alpha1 "github.com/knative/pkg/apis/duck/v1alpha1"
	"github.com/sirupsen/logrus"
	untypedcorev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/scheme"
	corev1 "k8s.io/client-go/kubernetes/typed/core/v1"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/tools/record"
)

const (
	controllerName = "prow-pipeline-crd"
	prowJobName    = "prowJobName"
	pipelineRun    = "PipelineRun"
	prowJob        = "ProwJob"
)

var (
	sleep = time.Sleep
)

type PipelineRunRequest struct {
	Labels      map[string]string     `json:"labels,omitempty"`
	ProwJobSpec prowjobv1.ProwJobSpec `json:"prowJobSpec,omitempty"`
}

type Pull struct {
	Author string `json:"author,omitempty"`
	Number string `json:"number,omitempty"`
	SHA    string `json:"sha,omitempty"`
}

type limiter interface {
	ShutDown()
	Get() (interface{}, bool)
	Done(interface{})
	Forget(interface{})
	AddRateLimited(interface{})
}

type controller struct {
	pjNamespace string
	pjc         prowjobset.Interface
	pipelines   map[string]pipelineConfig
	totURL      string

	pjLister   prowjoblisters.ProwJobLister
	pjInformer cache.SharedIndexInformer

	workqueue limiter

	recorder record.EventRecorder

	prowJobsDone  bool
	pipelinesDone map[string]bool
	wait          string
}

// PipelineRunResponse the results of triggering a pipeline run
type PipelineRunResponse struct {
	Resources []ObjectReference `json:"resources,omitempty"`
}

// ObjectReference represents a reference to a k8s resource
type ObjectReference struct {
	APIVersion string `json:"apiVersion" protobuf:"bytes,5,opt,name=apiVersion"`
	// Kind of the referent.
	// More info: https://git.k8s.io/community/contributors/devel/api-conventions.md#types-kinds
	Kind string `json:"kind" protobuf:"bytes,1,opt,name=kind"`
	// Name of the referent.
	// More info: http://kubernetes.io/docs/user-guide/identifiers#names
	Name string `json:"name" protobuf:"bytes,3,opt,name=name"`
}

// hasSynced returns true when every prowjob and pipeline informer has synced.
func (c *controller) hasSynced() bool {
	if !c.pjInformer.HasSynced() {
		if c.wait != "prowjobs" {
			c.wait = "prowjobs"
			ns := c.pjNamespace
			if ns == "" {
				ns = "controller's"
			}
			logrus.Infof("Waiting on prowjobs in %s namespace...", ns)
		}
		return false // still syncing prowjobs
	}
	if !c.prowJobsDone {
		c.prowJobsDone = true
		logrus.Info("Synced prow jobs")
	}
	if c.pipelinesDone == nil {
		c.pipelinesDone = map[string]bool{}
	}
	for n, cfg := range c.pipelines {
		if !cfg.informer.Informer().HasSynced() {
			if c.wait != n {
				c.wait = n
				logrus.Infof("Waiting on %s pipelines...", n)
			}
			return false // still syncing pipelines in at least one cluster
		} else if !c.pipelinesDone[n] {
			c.pipelinesDone[n] = true
			logrus.Infof("Synced %s pipelines", n)
		}
	}
	return true // Everyone is synced
}

func newController(kc kubernetes.Interface, pjc prowjobset.Interface, pji prowjobinfov1.ProwJobInformer, pipelineConfigs map[string]pipelineConfig, totURL, pjNamespace string, rl limiter) *controller {
	// Log to events
	prowjobscheme.AddToScheme(scheme.Scheme)
	eventBroadcaster := record.NewBroadcaster()
	eventBroadcaster.StartLogging(logrus.Infof)
	eventBroadcaster.StartRecordingToSink(&corev1.EventSinkImpl{Interface: kc.CoreV1().Events("")})
	recorder := eventBroadcaster.NewRecorder(scheme.Scheme, untypedcorev1.EventSource{Component: controllerName})

	// Create struct
	c := &controller{
		pjc:         pjc,
		pipelines:   pipelineConfigs,
		pjLister:    pji.Lister(),
		pjInformer:  pji.Informer(),
		workqueue:   rl,
		recorder:    recorder,
		totURL:      totURL,
		pjNamespace: pjNamespace,
	}

	logrus.Info("Setting up event handlers")

	// Reconcile whenever a prowjob changes
	pji.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {
			pj, ok := obj.(*prowjobv1.ProwJob)
			if !ok {
				logrus.Warnf("Ignoring bad prowjob add: %v", obj)
				return
			}
			c.enqueueKey(pj.Spec.Cluster, pj)
		},
		UpdateFunc: func(old, new interface{}) {
			pj, ok := new.(*prowjobv1.ProwJob)
			if !ok {
				logrus.Warnf("Ignoring bad prowjob update: %v", new)
				return
			}
			c.enqueueKey(pj.Spec.Cluster, pj)
		},
		DeleteFunc: func(obj interface{}) {
			pj, ok := obj.(*prowjobv1.ProwJob)
			if !ok {
				logrus.Warnf("Ignoring bad prowjob delete: %v", obj)
				return
			}
			c.enqueueKey(pj.Spec.Cluster, pj)
		},
	})

	for ctx, cfg := range pipelineConfigs {
		// Reconcile whenever a pipelinerun changes.
		ctx := ctx // otherwise it will change
		cfg.informer.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
			AddFunc: func(obj interface{}) {
				c.enqueueKey(ctx, obj)
			},
			UpdateFunc: func(old, new interface{}) {
				c.enqueueKey(ctx, new)
			},
			DeleteFunc: func(obj interface{}) {
				c.enqueueKey(ctx, obj)
			},
		})
	}

	return c
}

// Run starts threads workers, returning after receiving a stop signal.
func (c *controller) Run(threads int, stop <-chan struct{}) error {
	defer runtime.HandleCrash()
	defer c.workqueue.ShutDown()

	logrus.Info("Starting Pipeline controller")
	logrus.Info("Waiting for informer caches to sync")
	if ok := cache.WaitForCacheSync(stop, c.hasSynced); !ok {
		return fmt.Errorf("failed to wait for caches to sync")
	}

	logrus.Info("Starting workers")
	for i := 0; i < threads; i++ {
		go wait.Until(c.runWorker, time.Second, stop)
	}

	logrus.Info("Started workers")
	<-stop
	logrus.Info("Shutting down workers")
	return nil
}

// runWorker dequeues to reconcile, until the queue has closed.
func (c *controller) runWorker() {
	for {
		key, shutdown := c.workqueue.Get()
		if shutdown {
			return
		}
		func() {
			defer c.workqueue.Done(key)

			if err := reconcile(c, key.(string)); err != nil {
				runtime.HandleError(fmt.Errorf("failed to reconcile %s: %v", key, err))
				return // Do not forget so we retry later.
			}
			c.workqueue.Forget(key)
		}()
	}
}

// toKey returns context/namespace/name
func toKey(ctx, namespace, name, kind string) string {
	return strings.Join([]string{ctx, namespace, name, kind}, "/")
}

// fromKey converts toKey back into its parts
func fromKey(key string) (string, string, string, string, error) {
	parts := strings.Split(key, "/")
	if len(parts) != 4 {
		return "", "", "", "", fmt.Errorf("bad key: %q", key)
	}
	return parts[0], parts[1], parts[2], parts[3], nil
}

// enqueueKey schedules an item for reconciliation.
func (c *controller) enqueueKey(ctx string, obj interface{}) {
	switch o := obj.(type) {
	case *prowjobv1.ProwJob:
		//todo JR namespace can be empty so fails to create pipelineruns later
		ns := o.Spec.Namespace
		if ns == "" {
			ns = o.Namespace
		}
		c.workqueue.AddRateLimited(toKey(ctx, ns, o.Name, prowJob))
	case *pipelinev1alpha1.PipelineRun:
		c.workqueue.AddRateLimited(toKey(ctx, o.Namespace, o.Name, pipelineRun))
	default:
		logrus.Warnf("cannot enqueue unknown type %T: %v", o, obj)
		return
	}
}

type reconciler interface {
	getProwJob(name string) (*prowjobv1.ProwJob, error)
	getPipelineRun(context, namespace, name string) (*pipelinev1alpha1.PipelineRun, error)
	getPipelineRunWithSelector(context, namespace, selector string) (*pipelinev1alpha1.PipelineRun, error)
	deletePipelineRun(context, namespace, name string) error
	createPipelineRun(context, namespace string, b *pipelinev1alpha1.PipelineRun) (*pipelinev1alpha1.PipelineRun, error)
	createPipelineResource(context, namespace string, b *pipelinev1alpha1.PipelineResource) (*pipelinev1alpha1.PipelineResource, error)
	updateProwJob(pj *prowjobv1.ProwJob) (*prowjobv1.ProwJob, error)
	now() metav1.Time
	pipelineID(prowjobv1.ProwJob) (string, error)
	requestPipelineRun(context, namespace string, pj prowjobv1.ProwJob) (string, error)
}

func (c *controller) getPipelineConfig(ctx string) (pipelineConfig, error) {
	cfg, ok := c.pipelines[ctx]
	if !ok {
		defaultCtx := kube.DefaultClusterAlias
		defaultCfg, ok := c.pipelines[defaultCtx]
		if !ok {
			return pipelineConfig{}, fmt.Errorf("no cluster configuration found for default context %q", defaultCtx)
		}
		return defaultCfg, nil
	}
	return cfg, nil
}

func (c *controller) getProwJob(name string) (*prowjobv1.ProwJob, error) {
	return c.pjLister.ProwJobs(c.pjNamespace).Get(name)
}

func (c *controller) updateProwJob(pj *prowjobv1.ProwJob) (*prowjobv1.ProwJob, error) {
	logrus.Debugf("updateProwJob(%s)", pj.Name)
	return c.pjc.ProwV1().ProwJobs(c.pjNamespace).Update(pj)
}

func (c *controller) getPipelineRun(context, namespace, name string) (*pipelinev1alpha1.PipelineRun, error) {
	p, err := c.getPipelineConfig(context)
	if err != nil {
		return nil, err
	}
	return p.informer.Lister().PipelineRuns(namespace).Get(name)
}

func (c *controller) getPipelineRunWithSelector(context, namespace, selector string) (*pipelinev1alpha1.PipelineRun, error) {
	p, err := c.getPipelineConfig(context)
	if err != nil {
		return nil, err
	}

	label, err := labels.Parse(selector)
	if err != nil {
		return nil, fmt.Errorf("failed to parse selector %s", selector)
	}
	runs, err := p.informer.Lister().PipelineRuns(namespace).List(label)
	if err != nil {
		return nil, fmt.Errorf("failed to list pipelineruns with label %s", label.String())
	}
	if len(runs) > 1 {
		return nil, fmt.Errorf("%s pipelineruns found with label %s, expected only 1", string(len(runs)), label.String())
	}
	if len(runs) == 0 {
		return nil, apierrors.NewNotFound(pipelinev1alpha1.Resource("pipelinerun"), label.String())
	}
	return runs[0], nil
}

func (c *controller) deletePipelineRun(context, namespace, name string) error {
	logrus.Debugf("deletePipeline(%s,%s,%s)", context, namespace, name)
	p, err := c.getPipelineConfig(context)
	if err != nil {
		return err
	}
	return p.client.TektonV1alpha1().PipelineRuns(namespace).Delete(name, &metav1.DeleteOptions{})
}
func (c *controller) createPipelineRun(context, namespace string, b *pipelinev1alpha1.PipelineRun) (*pipelinev1alpha1.PipelineRun, error) {
	logrus.Debugf("createPipelineRun(%s,%s,%s)", context, namespace, b.Name)
	p, err := c.getPipelineConfig(context)
	if err != nil {
		return nil, err
	}
	return p.client.TektonV1alpha1().PipelineRuns(namespace).Create(b)
}

func (c *controller) createPipelineResource(context, namespace string, pr *pipelinev1alpha1.PipelineResource) (*pipelinev1alpha1.PipelineResource, error) {
	logrus.Debugf("createPipelineResource(%s,%s,%s)", context, namespace, pr.Name)
	p, err := c.getPipelineConfig(context)
	if err != nil {
		return nil, err
	}
	return p.client.TektonV1alpha1().PipelineResources(namespace).Create(pr)
}

func (c *controller) now() metav1.Time {
	return metav1.Now()
}

func (c *controller) pipelineID(pj prowjobv1.ProwJob) (string, error) {
	// todo not sure how to sort this out yet, but this is now Jenkins X specific
	branch := downwardapi.GetBranch(downwardapi.NewJobSpec(pj.Spec, "", pj.Name))
	if pj.Spec.Refs == nil {
		return "", fmt.Errorf("no spec refs")
	}
	if pj.Spec.Refs.Org == "" {
		return "", fmt.Errorf("spec refs org is empty")
	}
	if pj.Spec.Refs.Repo == "" {
		return "", fmt.Errorf("spec refs repo is empty")
	}
	jobName := fmt.Sprintf("%s/%s/%s", pj.Spec.Refs.Org, pj.Spec.Refs.Repo, branch)
	logrus.Infof("get build id for jobname: %s, from URL %s", jobName, c.totURL)
	return pjutil.GetBuildID(jobName, c.totURL)
}

var (
	groupVersionKind = schema.GroupVersionKind{
		Group:   prowjobv1.SchemeGroupVersion.Group,
		Version: prowjobv1.SchemeGroupVersion.Version,
		Kind:    "ProwJob",
	}
)

// reconcile ensures a knative-pipeline prowjob has a corresponding pipeline, updating the prowjob's status as the pipeline progresses.
func reconcile(c reconciler, key string) error {

	logrus.Debugf("reconcile: %s\n", key)
	// if pipelinerun and prowjob name are different we will need to lookup the prowjob name from the pipelinerun label
	ctx, namespace, name, kind, err := fromKey(key)
	if err != nil {
		runtime.HandleError(err)
		return nil
	}

	var wantPipelineRun bool
	var havePipelineRun bool
	// todo JR maybe we can combine this with wantPipelineRun or havePipelineRun above but this seems safer for now
	var reported bool
	var pj *prowjobv1.ProwJob
	var p *pipelinev1alpha1.PipelineRun
	var pr *pipelinev1alpha1.PipelineResource

	switch kind {
	// if this is a pipelinerun then get the prowjobname from a label, also use the name in the key to lookup pipelinerun
	case pipelineRun:
		p, err = c.getPipelineRun(ctx, namespace, name)
		if err != nil {
			return fmt.Errorf("no pipelinerun found with name %s: %v", name, err)
		}
		prowJobName := p.Labels[prowJobName]
		if prowJobName == "" {
			return fmt.Errorf("no prowjobname label for pipelinerun %s: %v", name, err)
		}

		pj, err = c.getProwJob(prowJobName)
		if err != nil {
			return fmt.Errorf("no matching prowjob for pipelinerun %s: %v", name, err)
		}

		havePipelineRun = true

		if p.DeletionTimestamp == nil {
			wantPipelineRun = true
		}

	// if this is a prowjob look for pipelineruns using the prowjobname as a selector
	case prowJob:
		pj, err = c.getProwJob(name)
		switch {
		case apierrors.IsNotFound(err):
			// Do not want pipeline
		case err != nil:
			return fmt.Errorf("get prowjob: %v", err)
		case pj.Spec.Agent != prowjobv1.TektonAgent:
			// Do not want a pipeline for this job
		case pj.Spec.Cluster != ctx:
			// need to disable this check as having issues when default current context is empty
			// Build is in wrong cluster, we do not want this build
			logrus.Warnf("%s found in context %s not %s", key, ctx, pj.Spec.Cluster)
		case pj.DeletionTimestamp == nil:
			wantPipelineRun = true
		}
		//make pointer so we can compare with nil
		if pj == nil {
			return fmt.Errorf("no prowjob found %s", name)
		}
		status := &pj.Status
		//add extra check as there is a delay for pipelinerun objects being created and we can get a watch event here
		//that updates the prowjob with the reporter status and still not have a pipelinerun which causes a duplicate
		//pipelinerun being requested
		if status != nil && status.PrevReportStates != nil && (status.PrevReportStates["github-reporter"] == kube.PendingState) {
			reported = true
		}

		selector := fmt.Sprintf("%s = %s", prowJobName, name)
		p, err = c.getPipelineRunWithSelector(ctx, namespace, selector)
		switch {
		case apierrors.IsNotFound(err):
			// Do not have a pipeline
		case err != nil:
			return fmt.Errorf("get pipelinerun %s: %v", key, err)
		}
		if p != nil {
			havePipelineRun = true
		}
	}

	// Should we create or delete this pipeline?
	switch {
	case !wantPipelineRun:
		if !havePipelineRun {
			if pj != nil && pj.Spec.Agent == prowjobv1.TektonAgent {
				logrus.Infof("Observed deleted %s", key)
			}
			return nil
		}
		switch v, ok := p.Labels[kube.CreatedByProw]; {
		case !ok, v != "true": // Not controlled by this
			return nil
		}
		logrus.Infof("Delete pipelines/%s", key)
		if err = c.deletePipelineRun(ctx, namespace, name); err != nil {
			return fmt.Errorf("delete pipeline: %v", err)
		}
		return nil
	case finalState(pj.Status.State):
		logrus.Infof("Observed finished %s", key)
		return nil
	case wantPipelineRun && !havePipelineRun && !reported && (pj.Spec.PipelineRunSpec == nil || pj.Spec.PipelineRunSpec.PipelineRef.Name == ""):
		// lets POST to Jenkins X pipeline runner
		pipelineRunName, err := c.requestPipelineRun(ctx, namespace, *pj)
		if err != nil {
			return fmt.Errorf("posting pipeline: %v", err)
		}
		// get the pipelinerun object that was just created
		p, err = c.getPipelineRun(ctx, namespace, pipelineRunName)
		if err != nil {
			return fmt.Errorf("finding pipeline %s: %v", name, err)
		}

	case wantPipelineRun && !reported && !havePipelineRun:
		logrus.Info("using embedded pipelinerun spec")
		id, err := c.pipelineID(*pj)
		if err != nil {
			return fmt.Errorf("failed to get pipeline id: %v", err)
		}

		if p, pr, err = makePipelineRun(*pj, id); err != nil {
			return fmt.Errorf("make pipeline: %v", err)
		}
		logrus.Infof("Create pipeline resource/%s", key)
		if pr, err = c.createPipelineResource(ctx, namespace, pr); err != nil {
			return fmt.Errorf("create pipelineresource: %v", err)
		}
		logrus.Infof("Create pipelinerun/%s", key)
		if p, err = c.createPipelineRun(ctx, namespace, p); err != nil {
			return fmt.Errorf("create pipelinerun: %v", err)
		}
	}

	if p == nil {
		return fmt.Errorf("no pipeline found or created for %s, wantPipelineRun was %v", key, wantPipelineRun)
	}

	haveState := pj.Status.State
	haveMsg := pj.Status.Description
	wantState, wantMsg := prowJobStatus(p.Status)
	if haveState != wantState || haveMsg != wantMsg {
		npj := pj.DeepCopy()
		if npj.Status.StartTime.IsZero() {
			npj.Status.StartTime = c.now()
		}
		if npj.Status.CompletionTime.IsZero() && finalState(wantState) {
			now := c.now()
			npj.Status.CompletionTime = &now
		}
		npj.Status.State = wantState
		npj.Status.Description = wantMsg
		logrus.Infof("Update %s /%s - %s - %v : %s", kind, key, pj.Name, npj.Labels, p.Name)
		if _, err = c.updateProwJob(npj); err != nil {
			return fmt.Errorf("update prow status: %v", err)
		}
	}
	return nil
}

// finalState returns true if the prowjob has already finished
func finalState(status prowjobv1.ProwJobState) bool {
	switch status {
	case "", prowjobv1.PendingState, prowjobv1.TriggeredState:
		return false
	}
	return true
}

// description computes the ProwJobStatus description for this condition or falling back to a default if none is provided.
func description(cond duckv1alpha1.Condition, fallback string) string {
	switch {
	case cond.Message != "":
		return cond.Message
	case cond.Reason != "":
		return cond.Reason
	}
	return fallback
}

const (
	descScheduling       = "scheduling"
	descInitializing     = "initializing"
	descRunning          = "running"
	descSucceeded        = "succeeded"
	descFailed           = "failed"
	descUnknown          = "unknown status"
	descMissingCondition = "missing end condition"
)

// prowJobStatus returns the desired state and description based on the pipeline status.
func prowJobStatus(ps pipelinev1alpha1.PipelineRunStatus) (prowjobv1.ProwJobState, string) {
	pcond := ps.GetCondition(duckv1alpha1.ConditionSucceeded)
	if pcond == nil {
		return prowjobv1.TriggeredState, descScheduling
	}
	cond := *pcond
	switch {
	case cond.Status == untypedcorev1.ConditionTrue:
		return prowjobv1.SuccessState, description(cond, descSucceeded)
	case cond.Status == untypedcorev1.ConditionFalse:
		return prowjobv1.FailureState, description(cond, descFailed)
	case cond.Status == untypedcorev1.ConditionUnknown:
		return prowjobv1.PendingState, description(cond, descRunning)
	}

	logrus.Warnf("Unknown condition %#v", cond)
	return prowjobv1.ErrorState, description(cond, descUnknown) // shouldn't happen
}

func pipelineMeta(pj prowjobv1.ProwJob) metav1.ObjectMeta {
	podLabels, annotations := decorate.LabelsAndAnnotationsForJob(pj)
	return metav1.ObjectMeta{
		Annotations: annotations,
		Name:        pj.Name,
		Namespace:   pj.Spec.Namespace,
		Labels:      podLabels,
	}
}

// pipelineEnv constructs the environment map for the job
func pipelineEnv(pj prowjobv1.ProwJob, pipelineID string) (map[string]string, error) {
	return downwardapi.EnvForSpec(downwardapi.NewJobSpec(pj.Spec, pipelineID, pj.Name))
}

// defaultEnv adds the map of environment variables to the container, except keys already defined.
func defaultEnv(c *untypedcorev1.Container, rawEnv map[string]string) {
	keys := sets.String{}
	for _, arg := range c.Env {
		keys.Insert(arg.Name)
	}
	for k, v := range rawEnv {
		if keys.Has(k) {
			continue
		}
		c.Env = append(c.Env, untypedcorev1.EnvVar{Name: k, Value: v})
	}
}

// makePipeline creates a pipeline from the prowjob, using the prowjob's pipelinespec.
func makePipelineRun(pj prowjobv1.ProwJob, buildID string) (*pipelinev1alpha1.PipelineRun, *pipelinev1alpha1.PipelineResource, error) {
	if pj.Spec.PipelineRunSpec == nil {
		return nil, nil, errors.New("nil PipelineSpec")
	}
	p := pipelinev1alpha1.PipelineRun{
		ObjectMeta: pipelineMeta(pj),
		Spec:       *pj.Spec.PipelineRunSpec,
	}

	// todo JR add envars or perhaps tekton input paramaters?
	//injectEnvironment(&p, rawEnv)

	// todo add in build id env var
	resourceBinding := pipelinev1alpha1.PipelineResourceBinding{
		// todo can we inject the build id here so that we get it in env vars on the steps???
	}
	p.Spec.Resources = append(p.Spec.Resources, resourceBinding)

	// todo this is github specific, is there a way to figure out the correct git provider?
	sourceURL := ""
	revision := ""
	if pj.Spec.Refs != nil {
		sourceURL = fmt.Sprintf("https://github.com/%s/%s.git", pj.Spec.Refs.Org, pj.Spec.Refs.Repo)

		// todo lets support batches of PRs
		if len(pj.Spec.Refs.Pulls) > 0 {
			revision = pj.Spec.Refs.Pulls[0].SHA
		} else {
			revision = pj.Spec.Refs.BaseSHA
		}
	}

	pr := pipelinev1alpha1.PipelineResource{
		ObjectMeta: pipelineMeta(pj),
		Spec: pipelinev1alpha1.PipelineResourceSpec{
			Type: pipelinev1alpha1.PipelineResourceTypeGit,
			Params: []pipelinev1alpha1.Param{
				{
					Name:  "source",
					Value: sourceURL,
				},
				{
					Name:  "revision",
					Value: revision,
				},
				{
					Name:  "build_id",
					Value: buildID,
				},
			},
		},
	}
	pr.ObjectMeta.OwnerReferences = pj.OwnerReferences

	// todo JR hack let's figure out a way to update the pipelnerun and tasks with the correct resource name
	pr.Name = "example-source"

	return &p, &pr, nil
}

// GetBuildID calls out to `tot` in order
// to vend build identifier for the job
func (c *controller) requestPipelineRun(context, namespace string, pj prowjobv1.ProwJob) (string, error) {
	pipelineURL, err := url.Parse("http://pipelinerunner")
	if err != nil {
		return "", fmt.Errorf("invalid pipelinerunner url: %v", err)
	}

	labels := map[string]string{}
	labels[prowJobName] = pj.Name
	labels[kube.CreatedByProw] = "true"
	payload := PipelineRunRequest{
		Labels:      labels,
		ProwJobSpec: pj.Spec,
	}

	jsonValue, _ := json.Marshal(payload)
	req, err := http.NewRequest("POST", pipelineURL.String(), bytes.NewBuffer(jsonValue))
	req.Header.Set("Content-Type", "application/json")

	client := http.DefaultClient
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("posting pipelinerunner: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		errorMessage := "got unexpected response from pipelinerunner"
		respData, err1 := ioutil.ReadAll(resp.Body)
		if err1 != nil {
			return "", fmt.Errorf("%s: %v", errorMessage, resp.Status)
		} else {
			return "", fmt.Errorf("%s: %v, %s", errorMessage, resp.Status, string(respData))
		}
	}

	respData, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("reading response body %v: %v", resp, err)
	}
	responses := PipelineRunResponse{}

	err = json.Unmarshal(respData, &responses)
	if err != nil {
		return "", fmt.Errorf("marshalling response %v", err)
	}

	for _, resource := range responses.Resources {
		if resource.Kind == "PipelineRun" {
			return resource.Name, nil
		}
	}

	return "", fmt.Errorf("no PipelineRun object found")
}
