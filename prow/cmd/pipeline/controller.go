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
)

var (
	sleep = time.Sleep
)

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
		// Reconcile whenever a pipeline changes.
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
func toKey(ctx, namespace, name string) string {
	return strings.Join([]string{ctx, namespace, name}, "/")
}

// fromKey converts toKey back into its parts
func fromKey(key string) (string, string, string, error) {
	parts := strings.Split(key, "/")
	if len(parts) != 3 {
		return "", "", "", fmt.Errorf("bad key: %q", key)
	}
	return parts[0], parts[1], parts[2], nil
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
		c.workqueue.AddRateLimited(toKey(ctx, ns, o.Name))
	case *pipelinev1alpha1.PipelineRun:
		c.workqueue.AddRateLimited(toKey(ctx, o.Namespace, o.Name))
	default:
		logrus.Warnf("cannot enqueue unknown type %T: %v", o, obj)
		return
	}
}

type reconciler interface {
	getProwJob(name string) (*prowjobv1.ProwJob, error)
	getPipelineRun(context, namespace, name string) (*pipelinev1alpha1.PipelineRun, error)
	deletePipelineRun(context, namespace, name string) error
	createPipelineRun(context, namespace string, b *pipelinev1alpha1.PipelineRun) (*pipelinev1alpha1.PipelineRun, error)
	updateProwJob(pj *prowjobv1.ProwJob) (*prowjobv1.ProwJob, error)
	now() metav1.Time
	pipelineID(prowjobv1.ProwJob) (string, error)
	postPipelineRunner(prowjobv1.ProwJob) error
}

func (c *controller) postPipelineRunner(pj prowjobv1.ProwJob) error {
	logrus.Info("a")
	sourceURL := fmt.Sprintf("https://github.com/%s/%s.git", pj.Spec.Refs.Org, pj.Spec.Refs.Repo)
	var revision string
	if len(pj.Spec.Refs.Pulls) > 0 {
		logrus.Info("b")
		revision = pj.Spec.Refs.Pulls[0].SHA
	} else {
		logrus.Info("c")
		revision = pj.Spec.Refs.BaseSHA
	}
	logrus.Info("d")
	return pipelineRequest(sourceURL, revision)
}

func (c *controller) getProwJob(name string) (*prowjobv1.ProwJob, error) {
	return c.pjLister.ProwJobs(c.pjNamespace).Get(name)
}

func (c *controller) updateProwJob(pj *prowjobv1.ProwJob) (*prowjobv1.ProwJob, error) {
	logrus.Debugf("updateProwJob(%s)", pj.Name)
	return c.pjc.ProwV1().ProwJobs(c.pjNamespace).Update(pj)
}

func (c *controller) getPipelineRun(context, namespace, name string) (*pipelinev1alpha1.PipelineRun, error) {
	b, ok := c.pipelines[context]
	if !ok {
		return nil, errors.New("context not found")
	}
	return b.informer.Lister().PipelineRuns(namespace).Get(name)
}
func (c *controller) deletePipelineRun(context, namespace, name string) error {
	logrus.Debugf("deletePipeline(%s,%s,%s)", context, namespace, name)
	b, ok := c.pipelines[context]
	if !ok {
		return errors.New("context not found")
	}
	return b.client.PipelineV1alpha1().PipelineRuns(namespace).Delete(name, &metav1.DeleteOptions{})
}
func (c *controller) createPipelineRun(context, namespace string, b *pipelinev1alpha1.PipelineRun) (*pipelinev1alpha1.PipelineRun, error) {
	logrus.Debugf("createPipelineRun(%s,%s,%s)", context, namespace, b.Name)
	bc, ok := c.pipelines[context]
	if !ok {
		return nil, errors.New("context not found")
	}
	return bc.client.PipelineV1alpha1().PipelineRuns(namespace).Create(b)
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
	ctx, namespace, name, err := fromKey(key)
	if err != nil {
		runtime.HandleError(err)
		return nil
	}

	// todo JR - need to nil out contexts to run on Jenkins X as default context is the same
	ctx = *new(string)
	var wantPipelineRun bool

	pj, err := c.getProwJob(name)
	switch {
	case apierrors.IsNotFound(err):
		// Do not want pipeline
	case err != nil:
		return fmt.Errorf("get prowjob: %v", err)
	case pj.Spec.Agent != prowjobv1.KnativePipelineRunAgent:
		// Do not want a pipeline for this job

		//todo JR we need to comment this out when run with Jenkins X
	//case pj.Spec.Cluster != ctx:
	//	// Pipeline is in wrong cluster, we do not want this pipeline
	//	logrus.Warnf("%s found in context %s not %s", key, ctx, pj.Spec.Cluster)
	case pj.DeletionTimestamp == nil:
		wantPipelineRun = true
	}

	var havePipeline bool

	// TODO(fejta): make trigger set the expected Namespace for the pod/pipeline.
	p, err := c.getPipelineRun(ctx, namespace, name)
	switch {
	case apierrors.IsNotFound(err):
		// Do not have a pipeline
	case err != nil:
		return fmt.Errorf("get pipeline %s: %v", key, err)
	case p.DeletionTimestamp == nil:
		havePipeline = true
	}
	logrus.Infof("spec %v", pj.Spec.PipelineRunSpec)
	logrus.Infof("wantPipelineRun %v", wantPipelineRun)
	// Should we create or delete this pipeline?
	switch {
	case !wantPipelineRun:
		if !havePipeline {
			if pj != nil && pj.Spec.Agent == prowjobv1.KnativePipelineRunAgent {
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
	case wantPipelineRun && (pj.Spec.PipelineRunSpec == nil || pj.Spec.PipelineRunSpec.PipelineRef.Name == ""):
		logrus.Info("generate pipeline resources from pipelinerunner")
		// lets POST to Jenkins X pipeline runner
		err = c.postPipelineRunner(*pj)
		if err != nil {
			return fmt.Errorf("posting pipeline: %v", err)
		}
	case wantPipelineRun && !havePipeline:

			logrus.Info("using embedded pipelinerun spec")
			id, err := c.pipelineID(*pj)
			if err != nil {
				return fmt.Errorf("failed to get pipeline id: %v", err)
			}
			if p, err = makePipelineRun(*pj, id); err != nil {
				return fmt.Errorf("make pipeline: %v", err)
			}
			logrus.Infof("Create pipelines/%s", key)
			if p, err = c.createPipelineRun(ctx, namespace, p); err != nil {
				return fmt.Errorf("create pipeline: %v", err)
			}

	}

	// Ensure prowjob status is correct
	haveState := pj.Status.State
	haveMsg := pj.Status.Description
	logrus.Infof("status %v", p.Status)
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
		logrus.Infof("Update prowjobs/%s", key)
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
	logrus.Infof("a %v", pcond)
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
func makePipelineRun(pj prowjobv1.ProwJob, pipelineID string) (*pipelinev1alpha1.PipelineRun, error) {
	if pj.Spec.PipelineRunSpec == nil {
		return nil, errors.New("nil PipelineSpec")
	}
	p := pipelinev1alpha1.PipelineRun{
		ObjectMeta: pipelineMeta(pj),
		Spec:       *pj.Spec.PipelineRunSpec,
	}

	// todo JR create a PipelineResource
	//err = injectSource(&p, pj)

	// todo JR add envars?
	//injectEnvironment(&p, rawEnv)

	return &p, nil
}

// GetBuildID calls out to `tot` in order
// to vend build identifier for the job
func pipelineRequest(repo, revision string) (error) {
	if repo == "" {
		return fmt.Errorf("not git URL found to request pipeline")
	}
	logrus.Info("e")
	var err error
	gitURL, err := url.Parse(repo)
	if err != nil {
		return fmt.Errorf("invalid git URL url: %v", err)
	}
	logrus.Info("f")
	pipelineURL, err := url.Parse("http://pipelinerunner")
	if err != nil {
		return fmt.Errorf("invalid pipelinerunner url: %v", err)
	}
	values := map[string]string{"gitUrl": gitURL.String(), "branch": revision}
	jsonValue, _ := json.Marshal(values)
	logrus.Infof("jsonValue %s", string(jsonValue))
	logrus.Infof("url %s", pipelineURL.String())
	resp, err := http.Post(pipelineURL.String(), "application/json", bytes.NewBuffer(jsonValue))

	if err != nil {
		return fmt.Errorf("posting pipelinerunner: %v", err)
	}
	logrus.Infof("j %v", resp)
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		err = fmt.Errorf("got unexpected response from tot: %v", resp.Status)
	}
	logrus.Info("k")
	// todo let's log the resources that are created using the response
	respData, err := ioutil.ReadAll(resp.Body)
	logrus.Infof("1 %v", respData)
	if err == nil {
		logrus.Info("l")
		return nil
	}
	logrus.Info("m")
	return err
}
