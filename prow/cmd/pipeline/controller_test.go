/*
Copyright 2019 The Kubernetes Authors.

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
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	duckv1alpha1 "github.com/knative/pkg/apis/duck/v1alpha1"
	"github.com/sirupsen/logrus"
	pipelinev1alpha1 "github.com/tektoncd/pipeline/pkg/apis/pipeline/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/diff"
	prowjobv1 "k8s.io/test-infra/prow/apis/prowjobs/v1"
	"k8s.io/test-infra/prow/kube"
	"k8s.io/test-infra/prow/pod-utils/decorate"
)

const (
	errorGetProwJob        = "error-get-prowjob"
	errorGetPipelineRun    = "error-get-pipeline"
	errorDeletePipelineRun = "error-delete-pipeline"
	errorCreatePipelineRun = "error-create-pipeline"
	errorUpdateProwJob     = "error-update-prowjob"
)

type fakeReconciler struct {
	jobs      map[string]prowjobv1.ProwJob
	pipelines map[string]pipelinev1alpha1.PipelineRun
	nows      metav1.Time
}

func (r *fakeReconciler) now() metav1.Time {
	fmt.Println(r.nows)
	return r.nows
}

const fakePJCtx = "prow-context"
const fakePJNS = "prow-job"

func (r *fakeReconciler) getProwJob(name string) (*prowjobv1.ProwJob, error) {
	logrus.Debugf("getProwJob: name=%s", name)
	if name == errorGetProwJob {
		return nil, errors.New("injected get prowjob error")
	}
	k := toKey(fakePJCtx, fakePJNS, name, prowJob)
	pj, present := r.jobs[k]
	if !present {
		return nil, apierrors.NewNotFound(prowjobv1.Resource("ProwJob"), name)
	}
	return &pj, nil
}

func (r *fakeReconciler) updateProwJob(pj *prowjobv1.ProwJob) (*prowjobv1.ProwJob, error) {
	logrus.Debugf("updateProwJob: name=%s", pj.GetName())
	if pj.Name == errorUpdateProwJob {
		return nil, errors.New("injected update prowjob error")
	}
	if pj == nil {
		return nil, errors.New("nil prowjob")
	}
	k := toKey(fakePJCtx, fakePJNS, pj.Name, prowJob)
	if _, present := r.jobs[k]; !present {
		return nil, apierrors.NewNotFound(prowjobv1.Resource("ProwJob"), pj.Name)
	}
	r.jobs[k] = *pj
	return pj, nil
}

func (r *fakeReconciler) patchProwJob(pj *prowjobv1.ProwJob) error {
	_, err := r.updateProwJob(pj)
	return err
}

func (r *fakeReconciler) getPipelineRun(context, namespace, name string) (*pipelinev1alpha1.PipelineRun, error) {
	logrus.Debugf("getPipelineRun: ctx=%s, ns=%s, name=%s", context, namespace, name)
	if namespace == errorGetPipelineRun {
		return nil, errors.New("injected create pipeline error")
	}
	k := toKey(context, namespace, name, pipelineRun)
	p, present := r.pipelines[k]
	if !present {
		return nil, apierrors.NewNotFound(pipelinev1alpha1.Resource("PipelineRun"), name)
	}
	return &p, nil
}
func (r *fakeReconciler) deletePipelineRun(context, namespace, name string) error {
	logrus.Debugf("deletePipelineRun: ctx=%s, ns=%s, name=%s", context, namespace, name)
	if namespace == errorDeletePipelineRun {
		return errors.New("injected create pipeline error")
	}
	k := toKey(context, namespace, name, pipelineRun)
	if _, present := r.pipelines[k]; !present {
		return apierrors.NewNotFound(pipelinev1alpha1.Resource("PipelineRun"), name)
	}
	delete(r.pipelines, k)
	return nil
}

func (r *fakeReconciler) createPipelineRun(context, namespace string, p *pipelinev1alpha1.PipelineRun) (*pipelinev1alpha1.PipelineRun, error) {
	logrus.Debugf("createPipelineRun: ctx=%s, ns=%s", context, namespace)
	if p == nil {
		return nil, errors.New("nil pipeline")
	}
	if namespace == errorCreatePipelineRun {
		return nil, errors.New("injected create pipeline error")
	}
	k := toKey(context, namespace, p.Name, pipelineRun)
	if _, alreadyExists := r.pipelines[k]; alreadyExists {
		return nil, apierrors.NewAlreadyExists(prowjobv1.Resource("ProwJob"), p.Name)
	}
	r.pipelines[k] = *p
	return p, nil
}

func (r *fakeReconciler) getPipelineRunsWithSelector(context, namespace, selector string) ([]*pipelinev1alpha1.PipelineRun, error) {
	parts := strings.Split(selector, "=")
	if len(parts) != 2 {
		return nil, fmt.Errorf("invalid selector: %s", selector)
	}
	pjName := strings.TrimSpace(parts[1])
	var runs []*pipelinev1alpha1.PipelineRun
	for _, p := range r.pipelines {
		labels := p.Labels
		for label, value := range labels {
			if label == kube.ProwJobIDLabel && value == pjName {
				k := toKey(context, namespace, p.Name, pipelineRun)
				p, ok := r.pipelines[k]
				if !ok {
					return nil, apierrors.NewNotFound(pipelinev1alpha1.Resource("PipelineRun"), pjName)
				}
				runs = append(runs, &p)
			}
		}
	}
	if len(runs) > 0 {
		if len(runs) > 1 {
			sort.Slice(runs, func(i, j int) bool {
				return runs[i].CreationTimestamp.Before(&runs[j].CreationTimestamp)
			})
		}
		return runs, nil
	}
	return nil, apierrors.NewNotFound(pipelinev1alpha1.Resource("PipelineRun"), pjName)
}

func (r *fakeReconciler) pipelineID(pj prowjobv1.ProwJob) (string, error) {
	return "7777777777", nil
}

func (r *fakeReconciler) createPipelineResource(context, namespace string, pr *pipelinev1alpha1.PipelineResource) (*pipelinev1alpha1.PipelineResource, error) {
	logrus.Debugf("createPipelineResource: ctx=%s, ns=%s, name=%s", context, namespace, pr.GetName())
	return pr, nil
}

func (r *fakeReconciler) requestPipelineRun(context, namespace string, pj prowjobv1.ProwJob) (string, error) {
	logrus.Debugf("requestPipelineRun: ctx=%s, ns=%s, pj=%s", context, namespace, pj.GetName())
	pr := makePipelineGitResource(pj)
	p, err := makePipelineRun(pj, "1", pr)
	if err != nil {
		return "", err
	}
	if p == nil {
		return "", errors.New("nil pipeline")
	}
	if namespace == errorCreatePipelineRun {
		return "", errors.New("injected request pipeline error")
	}
	k := toKey(context, namespace, p.Name, pipelineRun)
	rp, ok := r.pipelines[k]
	if ok {
		return rp.Name, nil
	}
	r.pipelines[k] = *p
	return p.Name, nil
}

func (r *fakeReconciler) getProwJobURL(prowjobv1.ProwJob) string {
	return ""
}

type fakeLimiter struct {
	added string
}

func (fl *fakeLimiter) ShutDown() {}
func (fl *fakeLimiter) ShuttingDown() bool {
	return false
}
func (fl *fakeLimiter) Get() (interface{}, bool) {
	return "not implemented", true
}
func (fl *fakeLimiter) Done(interface{})   {}
func (fl *fakeLimiter) Forget(interface{}) {}
func (fl *fakeLimiter) AddRateLimited(a interface{}) {
	fl.added = a.(string)
}
func (fl *fakeLimiter) Add(a interface{}) {
	fl.added = a.(string)
}
func (fl *fakeLimiter) AddAfter(a interface{}, d time.Duration) {
	fl.added = a.(string)
}
func (fl *fakeLimiter) Len() int {
	return 0
}
func (fl *fakeLimiter) NumRequeues(item interface{}) int {
	return 0
}

func TestEnqueueKey(t *testing.T) {
	cases := []struct {
		name     string
		context  string
		obj      interface{}
		expected string
	}{
		{
			name:    "enqueue pipeline directly",
			context: "hey",
			obj: &pipelinev1alpha1.PipelineRun{
				ObjectMeta: metav1.ObjectMeta{
					Namespace: "foo",
					Name:      "bar",
				},
			},
			expected: toKey("hey", "foo", "bar", pipelineRun),
		},
		{
			name:    "enqueue prowjob's spec namespace",
			context: "rolo",
			obj: &prowjobv1.ProwJob{
				ObjectMeta: metav1.ObjectMeta{
					Namespace: "default",
					Name:      "dude",
				},
				Spec: prowjobv1.ProwJobSpec{
					Namespace: "tomassi",
				},
			},
			expected: toKey("rolo", "tomassi", "dude", prowJob),
		},
		{
			name:    "ignore random object",
			context: "foo",
			obj:     "bar",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var fl fakeLimiter
			c := controller{
				workqueue: &fl,
			}
			c.enqueueKey(tc.context, tc.obj)
			if !reflect.DeepEqual(fl.added, tc.expected) {
				t.Errorf("%q != expected %q", fl.added, tc.expected)
			}
		})
	}
}

func TestReconcile(t *testing.T) {
	logrus.SetLevel(logrus.DebugLevel)
	now := metav1.Now()
	pipelineSpec := pipelinev1alpha1.PipelineRunSpec{}
	noJobChange := func(pj prowjobv1.ProwJob, _ pipelinev1alpha1.PipelineRun) prowjobv1.ProwJob {
		return pj
	}
	noPipelineRunChange := func(_ prowjobv1.ProwJob, p pipelinev1alpha1.PipelineRun) pipelinev1alpha1.PipelineRun {
		return p
	}
	cases := []struct {
		name                 string
		namespace            string
		context              string
		observedJob          *prowjobv1.ProwJob
		observedPipelineRuns []*pipelinev1alpha1.PipelineRun
		expectedJob          func(prowjobv1.ProwJob, pipelinev1alpha1.PipelineRun) prowjobv1.ProwJob
		expectedPipelineRun  func(prowjobv1.ProwJob, pipelinev1alpha1.PipelineRun) pipelinev1alpha1.PipelineRun
		err                  bool
	}{{
		name: "new prow job creates pipeline",
		observedJob: &prowjobv1.ProwJob{
			Spec: prowjobv1.ProwJobSpec{
				Agent:           prowjobv1.TektonAgent,
				PipelineRunSpec: &pipelineSpec,
			},
		},
		expectedJob: func(pj prowjobv1.ProwJob, _ pipelinev1alpha1.PipelineRun) prowjobv1.ProwJob {
			pj.Status = prowjobv1.ProwJobStatus{
				StartTime:   now,
				State:       prowjobv1.TriggeredState,
				Description: descScheduling,
				BuildID:     "1",
			}
			return pj
		},
		expectedPipelineRun: func(pj prowjobv1.ProwJob, _ pipelinev1alpha1.PipelineRun) pipelinev1alpha1.PipelineRun {
			pj.Spec.Type = prowjobv1.PeriodicJob
			pr := makePipelineGitResource(pj)
			p, err := makePipelineRun(pj, "1", pr)
			if err != nil {
				panic(err)
			}
			return *p
		},
	},
		{
			name: "do not create pipeline run for failed prowjob",
			observedJob: &prowjobv1.ProwJob{
				Spec: prowjobv1.ProwJobSpec{
					Agent:           prowjobv1.TektonAgent,
					PipelineRunSpec: &pipelineSpec,
				},
				Status: prowjobv1.ProwJobStatus{
					State: prowjobv1.FailureState,
				},
			},
			expectedJob: noJobChange,
		},
		{
			name: "do not create pipeline run for successful prowjob",
			observedJob: &prowjobv1.ProwJob{
				Spec: prowjobv1.ProwJobSpec{
					Agent:           prowjobv1.TektonAgent,
					PipelineRunSpec: &pipelineSpec,
				},
				Status: prowjobv1.ProwJobStatus{
					State: prowjobv1.SuccessState,
				},
			},
			expectedJob: noJobChange,
		},
		{
			name: "do not create pipeline run for aborted prowjob",
			observedJob: &prowjobv1.ProwJob{
				Spec: prowjobv1.ProwJobSpec{
					Agent:           prowjobv1.TektonAgent,
					PipelineRunSpec: &pipelineSpec,
				},
				Status: prowjobv1.ProwJobStatus{
					State: prowjobv1.AbortedState,
				},
			},
			expectedJob: noJobChange,
		},
		{
			name: "delete pipeline run after deleting prowjob",
			observedPipelineRuns: func() []*pipelinev1alpha1.PipelineRun {
				pj := prowjobv1.ProwJob{}
				pj.Spec.Type = prowjobv1.PeriodicJob
				pj.Spec.PipelineRunSpec = &pipelinev1alpha1.PipelineRunSpec{}
				pr := makePipelineGitResource(pj)
				p, err := makePipelineRun(pj, "7", pr)
				if err != nil {
					panic(err)
				}
				p.CreationTimestamp = metav1.Now()
				return []*pipelinev1alpha1.PipelineRun{p}
			}(),
			err: true,
		},
		{
			name: "do not delete deleted pipeline runs",
			observedPipelineRuns: func() []*pipelinev1alpha1.PipelineRun {
				pj := prowjobv1.ProwJob{}
				pj.Spec.Type = prowjobv1.PeriodicJob
				pj.Spec.PipelineRunSpec = &pipelinev1alpha1.PipelineRunSpec{}
				pr := makePipelineGitResource(pj)
				p, err := makePipelineRun(pj, "6", pr)
				p.DeletionTimestamp = &now
				if err != nil {
					panic(err)
				}
				p.CreationTimestamp = metav1.Now()

				return []*pipelinev1alpha1.PipelineRun{p}
			}(),
			expectedPipelineRun: noPipelineRunChange,
			err:                 true,
		},
		{
			name: "only delete pipeline runs created by controller",
			observedPipelineRuns: func() []*pipelinev1alpha1.PipelineRun {
				pj := prowjobv1.ProwJob{}
				pj.Spec.Type = prowjobv1.PeriodicJob
				pj.Spec.PipelineRunSpec = &pipelinev1alpha1.PipelineRunSpec{}
				pr := makePipelineGitResource(pj)
				p, err := makePipelineRun(pj, "9999", pr)
				if err != nil {
					panic(err)
				}
				p.CreationTimestamp = metav1.Now()
				delete(p.Labels, kube.CreatedByProw)
				return []*pipelinev1alpha1.PipelineRun{p}
			}(),
			expectedPipelineRun: noPipelineRunChange,
			err:                 true,
		},
		{
			name:    "do not delete pipeline run in the wrong cluster",
			context: "wrong-cluster",
			observedJob: &prowjobv1.ProwJob{
				Spec: prowjobv1.ProwJobSpec{
					Agent:   prowjobv1.TektonAgent,
					Cluster: "target-cluster",
					PipelineRunSpec: &pipelinev1alpha1.PipelineRunSpec{
						ServiceAccount: "robot",
					},
				},
				Status: prowjobv1.ProwJobStatus{
					State:       prowjobv1.PendingState,
					StartTime:   metav1.Now(),
					Description: "fancy",
				},
			},
			observedPipelineRuns: func() []*pipelinev1alpha1.PipelineRun {
				pj := prowjobv1.ProwJob{}
				pj.Spec.Type = prowjobv1.PeriodicJob
				pj.Spec.Agent = prowjobv1.TektonAgent
				pj.Spec.PipelineRunSpec = &pipelineSpec
				pr := makePipelineGitResource(pj)
				p, err := makePipelineRun(pj, "5", pr)
				if err != nil {
					panic(err)
				}
				p.CreationTimestamp = metav1.Now()
				return []*pipelinev1alpha1.PipelineRun{p}
			}(),
			expectedJob:         noJobChange,
			expectedPipelineRun: noPipelineRunChange,
		},
		{
			name:    "ignore random pipeline run in the wrong cluster",
			context: "wrong-cluster",
			observedJob: &prowjobv1.ProwJob{
				Spec: prowjobv1.ProwJobSpec{
					Agent:   prowjobv1.TektonAgent,
					Cluster: "target-cluster",
					PipelineRunSpec: &pipelinev1alpha1.PipelineRunSpec{
						ServiceAccount: "robot",
					},
				},
				Status: prowjobv1.ProwJobStatus{
					State:       prowjobv1.PendingState,
					StartTime:   metav1.Now(),
					Description: "fancy",
				},
			},
			observedPipelineRuns: func() []*pipelinev1alpha1.PipelineRun {
				pj := prowjobv1.ProwJob{}
				pj.Spec.Type = prowjobv1.PeriodicJob
				pj.Spec.Agent = prowjobv1.TektonAgent
				pj.Spec.PipelineRunSpec = &pipelineSpec
				pr := makePipelineGitResource(pj)
				p, err := makePipelineRun(pj, "5", pr)
				if err != nil {
					panic(err)
				}
				p.CreationTimestamp = metav1.Now()
				delete(p.Labels, kube.CreatedByProw)
				return []*pipelinev1alpha1.PipelineRun{p}
			}(),
			expectedJob:         noJobChange,
			expectedPipelineRun: noPipelineRunChange,
		},
		{
			name: "update job status if pipeline run resets",
			observedJob: &prowjobv1.ProwJob{
				Spec: prowjobv1.ProwJobSpec{
					Agent: prowjobv1.TektonAgent,
					PipelineRunSpec: &pipelinev1alpha1.PipelineRunSpec{
						ServiceAccount: "robot",
					},
				},
				Status: prowjobv1.ProwJobStatus{
					State:       prowjobv1.PendingState,
					StartTime:   metav1.Now(),
					Description: "fancy",
				},
			},
			observedPipelineRuns: func() []*pipelinev1alpha1.PipelineRun {
				pj := prowjobv1.ProwJob{}
				pj.Spec.Type = prowjobv1.PeriodicJob
				pj.Spec.Agent = prowjobv1.TektonAgent
				pj.Spec.PipelineRunSpec = &pipelinev1alpha1.PipelineRunSpec{
					ServiceAccount: "robot",
				}
				pr := makePipelineGitResource(pj)
				p, err := makePipelineRun(pj, "5", pr)
				if err != nil {
					panic(err)
				}
				p.CreationTimestamp = metav1.Now()
				return []*pipelinev1alpha1.PipelineRun{p}
			}(),
			expectedJob: func(pj prowjobv1.ProwJob, _ pipelinev1alpha1.PipelineRun) prowjobv1.ProwJob {
				pj.Status.State = prowjobv1.TriggeredState
				pj.Status.Description = descScheduling
				return pj
			},
			expectedPipelineRun: noPipelineRunChange,
		},
		{
			name: "prowjob goes triggered  when pipeline run starts",
			observedJob: &prowjobv1.ProwJob{
				Spec: prowjobv1.ProwJobSpec{
					Agent:           prowjobv1.TektonAgent,
					PipelineRunSpec: &pipelineSpec,
				},
				Status: prowjobv1.ProwJobStatus{
					State:       prowjobv1.PendingState,
					Description: "fancy",
				},
			},
			observedPipelineRuns: func() []*pipelinev1alpha1.PipelineRun {
				pj := prowjobv1.ProwJob{}
				pj.Spec.Type = prowjobv1.PeriodicJob
				pj.Spec.Agent = prowjobv1.TektonAgent
				pj.Spec.PipelineRunSpec = &pipelineSpec
				pr := makePipelineGitResource(pj)
				p, err := makePipelineRun(pj, "1", pr)
				if err != nil {
					panic(err)
				}
				p.Status.SetCondition(&duckv1alpha1.Condition{
					Type:    duckv1alpha1.ConditionReady,
					Message: "hello",
				})
				p.CreationTimestamp = metav1.Now()
				return []*pipelinev1alpha1.PipelineRun{p}
			}(),
			expectedJob: func(pj prowjobv1.ProwJob, _ pipelinev1alpha1.PipelineRun) prowjobv1.ProwJob {
				pj.Status = prowjobv1.ProwJobStatus{
					StartTime:   now,
					State:       prowjobv1.TriggeredState,
					Description: "scheduling",
				}
				return pj
			},
			expectedPipelineRun: noPipelineRunChange,
		},
		{
			name: "prowjob succeeds when run pipeline succeeds",
			observedJob: &prowjobv1.ProwJob{
				Spec: prowjobv1.ProwJobSpec{
					Agent:           prowjobv1.TektonAgent,
					PipelineRunSpec: &pipelineSpec,
				},
				Status: prowjobv1.ProwJobStatus{
					State:       prowjobv1.PendingState,
					Description: "fancy",
				},
			},
			observedPipelineRuns: func() []*pipelinev1alpha1.PipelineRun {
				pj := prowjobv1.ProwJob{}
				pj.Spec.Type = prowjobv1.PeriodicJob
				pj.Spec.Agent = prowjobv1.TektonAgent
				pj.Spec.PipelineRunSpec = &pipelineSpec
				pr := makePipelineGitResource(pj)
				p, err := makePipelineRun(pj, "22", pr)
				if err != nil {
					panic(err)
				}
				p.Status.SetCondition(&duckv1alpha1.Condition{
					Type:    duckv1alpha1.ConditionSucceeded,
					Status:  corev1.ConditionTrue,
					Message: "hello",
				})
				p.CreationTimestamp = metav1.Now()
				return []*pipelinev1alpha1.PipelineRun{p}
			}(),
			expectedJob: func(pj prowjobv1.ProwJob, _ pipelinev1alpha1.PipelineRun) prowjobv1.ProwJob {
				pj.Status = prowjobv1.ProwJobStatus{
					StartTime:      now,
					CompletionTime: &now,
					State:          prowjobv1.SuccessState,
					Description:    "hello",
				}
				return pj
			},
			expectedPipelineRun: noPipelineRunChange,
		},
		{
			name: "prowjob fails when pipeline run fails",
			observedJob: &prowjobv1.ProwJob{
				Spec: prowjobv1.ProwJobSpec{
					Agent:           prowjobv1.TektonAgent,
					PipelineRunSpec: &pipelineSpec,
				},
				Status: prowjobv1.ProwJobStatus{
					State:       prowjobv1.PendingState,
					Description: "fancy",
				},
			},
			observedPipelineRuns: func() []*pipelinev1alpha1.PipelineRun {
				pj := prowjobv1.ProwJob{}
				pj.Spec.Type = prowjobv1.PeriodicJob
				pj.Spec.Agent = prowjobv1.TektonAgent
				pj.Spec.PipelineRunSpec = &pipelineSpec
				pr := makePipelineGitResource(pj)
				p, err := makePipelineRun(pj, "21", pr)
				if err != nil {
					panic(err)
				}
				p.Status.SetCondition(&duckv1alpha1.Condition{
					Type:    duckv1alpha1.ConditionSucceeded,
					Status:  corev1.ConditionFalse,
					Message: "hello",
				})
				p.CreationTimestamp = metav1.Now()
				return []*pipelinev1alpha1.PipelineRun{p}
			}(),
			expectedJob: func(pj prowjobv1.ProwJob, _ pipelinev1alpha1.PipelineRun) prowjobv1.ProwJob {
				pj.Status = prowjobv1.ProwJobStatus{
					StartTime:      now,
					CompletionTime: &now,
					State:          prowjobv1.FailureState,
					Description:    "hello",
				}
				return pj
			},
			expectedPipelineRun: noPipelineRunChange,
		},
		{
			name:      "error when we cannot get prowjob",
			namespace: errorGetProwJob,
			err:       true,
			observedJob: &prowjobv1.ProwJob{
				Spec: prowjobv1.ProwJobSpec{
					Agent:           prowjobv1.TektonAgent,
					PipelineRunSpec: &pipelineSpec,
				},
				Status: prowjobv1.ProwJobStatus{
					State:       prowjobv1.PendingState,
					Description: "fancy",
				},
			},
		},
		{
			name:      "error when we cannot get pipeline run",
			namespace: errorGetPipelineRun,
			err:       true,
			observedPipelineRuns: func() []*pipelinev1alpha1.PipelineRun {
				pj := prowjobv1.ProwJob{}
				pj.Spec.Type = prowjobv1.PeriodicJob
				pj.Spec.Agent = prowjobv1.TektonAgent
				pj.Spec.PipelineRunSpec = &pipelineSpec
				pr := makePipelineGitResource(pj)
				p, err := makePipelineRun(pj, "-72", pr)
				if err != nil {
					panic(err)
				}
				p.Status.SetCondition(&duckv1alpha1.Condition{
					Type:    duckv1alpha1.ConditionSucceeded,
					Status:  corev1.ConditionTrue,
					Message: "hello",
				})
				p.CreationTimestamp = metav1.Now()
				return []*pipelinev1alpha1.PipelineRun{p}
			}(),
		},
		{
			name:      "error when we cannot delete pipeline run",
			namespace: errorDeletePipelineRun,
			err:       true,
			observedPipelineRuns: func() []*pipelinev1alpha1.PipelineRun {
				pj := prowjobv1.ProwJob{}
				pj.Spec.Type = prowjobv1.PeriodicJob
				pj.Spec.PipelineRunSpec = &pipelinev1alpha1.PipelineRunSpec{}
				pr := makePipelineGitResource(pj)
				p, err := makePipelineRun(pj, "44", pr)
				if err != nil {
					panic(err)
				}
				p.CreationTimestamp = metav1.Now()
				return []*pipelinev1alpha1.PipelineRun{p}
			}(),
		},
		{
			name:      "set job in error state when we cannot create pipeline run",
			namespace: errorCreatePipelineRun,
			err:       false,
			observedJob: &prowjobv1.ProwJob{
				Spec: prowjobv1.ProwJobSpec{
					Agent:           prowjobv1.TektonAgent,
					PipelineRunSpec: &pipelineSpec,
				},
			},
			expectedJob: func(pj prowjobv1.ProwJob, _ pipelinev1alpha1.PipelineRun) prowjobv1.ProwJob {
				pj.Status = prowjobv1.ProwJobStatus{
					StartTime:      now,
					CompletionTime: now.DeepCopy(),
					State:          prowjobv1.ErrorState,
					Description:    "injected request pipeline error",
				}
				return pj
			},
		},
		{
			name: "set job in error state when pipelinerunspec is nil and request pipeline run retuns an error",
			err:  false,
			observedJob: &prowjobv1.ProwJob{
				Spec: prowjobv1.ProwJobSpec{
					Agent:           prowjobv1.TektonAgent,
					PipelineRunSpec: nil,
				},
				Status: prowjobv1.ProwJobStatus{
					State: prowjobv1.TriggeredState,
				},
			},
			expectedJob: func(pj prowjobv1.ProwJob, _ pipelinev1alpha1.PipelineRun) prowjobv1.ProwJob {
				pj.Status = prowjobv1.ProwJobStatus{
					StartTime:      now,
					CompletionTime: now.DeepCopy(),
					State:          prowjobv1.ErrorState,
					Description:    "no PipelineRunSpec defined",
				}
				return pj
			},
		},
		{
			name:      "error when we cannot update prowjob",
			namespace: errorUpdateProwJob,
			err:       true,
			observedJob: &prowjobv1.ProwJob{
				Spec: prowjobv1.ProwJobSpec{
					Agent:           prowjobv1.TektonAgent,
					PipelineRunSpec: &pipelineSpec,
				},
				Status: prowjobv1.ProwJobStatus{
					State:       prowjobv1.PendingState,
					Description: "fancy",
				},
			},
			observedPipelineRuns: func() []*pipelinev1alpha1.PipelineRun {
				pj := prowjobv1.ProwJob{}
				pj.Spec.Type = prowjobv1.PeriodicJob
				pj.Spec.Agent = prowjobv1.TektonAgent
				pj.Spec.PipelineRunSpec = &pipelineSpec
				pr := makePipelineGitResource(pj)
				p, err := makePipelineRun(pj, "42", pr)
				if err != nil {
					panic(err)
				}
				p.Status.SetCondition(&duckv1alpha1.Condition{
					Type:    duckv1alpha1.ConditionSucceeded,
					Status:  corev1.ConditionTrue,
					Message: "hello",
				})
				p.CreationTimestamp = metav1.Now()
				return []*pipelinev1alpha1.PipelineRun{p}
			}(),
		},
		{
			name: "with successful metapipeline and generated pipeline",
			observedJob: &prowjobv1.ProwJob{
				Spec: prowjobv1.ProwJobSpec{
					Agent:           prowjobv1.TektonAgent,
					PipelineRunSpec: &pipelineSpec,
				},
				Status: prowjobv1.ProwJobStatus{
					State:       prowjobv1.PendingState,
					Description: "fancy",
				},
			},
			observedPipelineRuns: func() []*pipelinev1alpha1.PipelineRun {
				pj := prowjobv1.ProwJob{}
				pj.Spec.Type = prowjobv1.PeriodicJob
				pj.Spec.Agent = prowjobv1.TektonAgent
				pj.Spec.PipelineRunSpec = &pipelinev1alpha1.PipelineRunSpec{
					ServiceAccount: "robot",
				}
				pr := makePipelineGitResource(pj)
				metaP, err := makePipelineRunWithPrefix(pj, "5", pr, "meta")
				if err != nil {
					panic(err)
				}
				metaP.Status.SetCondition(&duckv1alpha1.Condition{
					Type:    duckv1alpha1.ConditionSucceeded,
					Status:  corev1.ConditionTrue,
					Reason:  "Succeeded",
					Message: "Metapipeline",
				})
				metaP.CreationTimestamp = metav1.Now()
				execP, err := makePipelineRun(pj, "5", pr)
				if err != nil {
					panic(err)
				}
				execP.Status.SetCondition(&duckv1alpha1.Condition{
					Type:    duckv1alpha1.ConditionSucceeded,
					Status:  corev1.ConditionTrue,
					Reason:  "Succeeded",
					Message: "Exec pipeline",
				})
				execP.CreationTimestamp = metav1.Now()
				return []*pipelinev1alpha1.PipelineRun{metaP, execP}
			}(),
			expectedJob: func(pj prowjobv1.ProwJob, _ pipelinev1alpha1.PipelineRun) prowjobv1.ProwJob {
				pj.Status = prowjobv1.ProwJobStatus{
					StartTime:      now,
					CompletionTime: &now,
					State:          prowjobv1.SuccessState,
					Description:    "Exec pipeline",
				}
				return pj
			},
			expectedPipelineRun: noPipelineRunChange,
		},
		{
			name: "with successful metapipeline and running generated pipeline",
			observedJob: &prowjobv1.ProwJob{
				Spec: prowjobv1.ProwJobSpec{
					Agent:           prowjobv1.TektonAgent,
					PipelineRunSpec: &pipelineSpec,
				},
				Status: prowjobv1.ProwJobStatus{
					State:       prowjobv1.PendingState,
					Description: "fancy",
				},
			},
			observedPipelineRuns: func() []*pipelinev1alpha1.PipelineRun {
				pj := prowjobv1.ProwJob{}
				pj.Spec.Type = prowjobv1.PeriodicJob
				pj.Spec.Agent = prowjobv1.TektonAgent
				pj.Spec.PipelineRunSpec = &pipelinev1alpha1.PipelineRunSpec{
					ServiceAccount: "robot",
				}
				pr := makePipelineGitResource(pj)
				metaP, err := makePipelineRunWithPrefix(pj, "5", pr, "meta")
				if err != nil {
					panic(err)
				}
				metaP.Status.SetCondition(&duckv1alpha1.Condition{
					Type:    duckv1alpha1.ConditionSucceeded,
					Status:  corev1.ConditionTrue,
					Reason:  "Succeeded",
					Message: "Metapipeline",
				})
				metaP.CreationTimestamp = metav1.Now()

				execP, err := makePipelineRun(pj, "5", pr)
				if err != nil {
					panic(err)
				}
				execP.Status.SetCondition(&duckv1alpha1.Condition{
					Type:    duckv1alpha1.ConditionSucceeded,
					Status:  corev1.ConditionUnknown,
					Reason:  "Running",
					Message: "Exec pipeline",
				})
				execP.CreationTimestamp = metav1.Now()
				return []*pipelinev1alpha1.PipelineRun{metaP, execP}
			}(),
			expectedJob: func(pj prowjobv1.ProwJob, _ pipelinev1alpha1.PipelineRun) prowjobv1.ProwJob {
				pj.Status = prowjobv1.ProwJobStatus{
					StartTime:   now,
					State:       prowjobv1.PendingState,
					Description: "Exec pipeline",
				}
				return pj
			},
			expectedPipelineRun: noPipelineRunChange,
		},
		{
			name: "with successful metapipeline and failed generated pipeline",
			observedJob: &prowjobv1.ProwJob{
				Spec: prowjobv1.ProwJobSpec{
					Agent:           prowjobv1.TektonAgent,
					PipelineRunSpec: &pipelineSpec,
				},
				Status: prowjobv1.ProwJobStatus{
					State:       prowjobv1.PendingState,
					Description: "fancy",
				},
			},
			observedPipelineRuns: func() []*pipelinev1alpha1.PipelineRun {
				pj := prowjobv1.ProwJob{}
				pj.Spec.Type = prowjobv1.PeriodicJob
				pj.Spec.Agent = prowjobv1.TektonAgent
				pj.Spec.PipelineRunSpec = &pipelinev1alpha1.PipelineRunSpec{
					ServiceAccount: "robot",
				}
				pr := makePipelineGitResource(pj)
				metaP, err := makePipelineRunWithPrefix(pj, "5", pr, "meta")
				if err != nil {
					panic(err)
				}
				metaP.Status.SetCondition(&duckv1alpha1.Condition{
					Type:    duckv1alpha1.ConditionSucceeded,
					Status:  corev1.ConditionTrue,
					Reason:  "Succeeded",
					Message: "Metapipeline",
				})
				metaP.CreationTimestamp = metav1.Now()
				execP, err := makePipelineRun(pj, "5", pr)
				if err != nil {
					panic(err)
				}
				execP.Status.SetCondition(&duckv1alpha1.Condition{
					Type:    duckv1alpha1.ConditionSucceeded,
					Status:  corev1.ConditionFalse,
					Reason:  "Failed",
					Message: "Exec pipeline",
				})
				execP.CreationTimestamp = metav1.Now()
				return []*pipelinev1alpha1.PipelineRun{metaP, execP}
			}(),
			expectedJob: func(pj prowjobv1.ProwJob, _ pipelinev1alpha1.PipelineRun) prowjobv1.ProwJob {
				pj.Status = prowjobv1.ProwJobStatus{
					StartTime:      now,
					CompletionTime: &now,
					State:          prowjobv1.FailureState,
					Description:    "Exec pipeline",
				}
				return pj
			},
			expectedPipelineRun: noPipelineRunChange,
		},
		{
			name: "with successful metapipeline and triggered generated pipeline",
			observedJob: &prowjobv1.ProwJob{
				Spec: prowjobv1.ProwJobSpec{
					Agent:           prowjobv1.TektonAgent,
					PipelineRunSpec: &pipelineSpec,
				},
				Status: prowjobv1.ProwJobStatus{
					State:       prowjobv1.PendingState,
					Description: "fancy",
				},
			},
			observedPipelineRuns: func() []*pipelinev1alpha1.PipelineRun {
				pj := prowjobv1.ProwJob{}
				pj.Spec.Type = prowjobv1.PeriodicJob
				pj.Spec.Agent = prowjobv1.TektonAgent
				pj.Spec.PipelineRunSpec = &pipelinev1alpha1.PipelineRunSpec{
					ServiceAccount: "robot",
				}
				pr := makePipelineGitResource(pj)
				metaP, err := makePipelineRunWithPrefix(pj, "5", pr, "meta")
				if err != nil {
					panic(err)
				}
				metaP.Status.SetCondition(&duckv1alpha1.Condition{
					Type:    duckv1alpha1.ConditionSucceeded,
					Status:  corev1.ConditionTrue,
					Reason:  "Succeeded",
					Message: "Metapipeline",
				})
				metaP.CreationTimestamp = metav1.Now()
				execP, err := makePipelineRun(pj, "5", pr)
				if err != nil {
					panic(err)
				}
				execP.Status.SetCondition(&duckv1alpha1.Condition{
					Type:    duckv1alpha1.ConditionReady,
					Message: "hello",
				})
				execP.CreationTimestamp = metav1.Now()
				return []*pipelinev1alpha1.PipelineRun{metaP, execP}
			}(),
			expectedJob: func(pj prowjobv1.ProwJob, _ pipelinev1alpha1.PipelineRun) prowjobv1.ProwJob {
				pj.Status = prowjobv1.ProwJobStatus{
					StartTime:   now,
					State:       prowjobv1.TriggeredState,
					Description: "scheduling",
				}
				return pj
			},
			expectedPipelineRun: noPipelineRunChange,
		},
		{
			name: "prowjob retries when pipeline run fails with race condition",
			observedJob: &prowjobv1.ProwJob{
				Spec: prowjobv1.ProwJobSpec{
					Agent:           prowjobv1.TektonAgent,
					PipelineRunSpec: &pipelineSpec,
				},
				Status: prowjobv1.ProwJobStatus{
					State:       prowjobv1.PendingState,
					Description: "fancy",
				},
			},
			observedPipelineRuns: func() []*pipelinev1alpha1.PipelineRun {
				pj := prowjobv1.ProwJob{}
				pj.Spec.Type = prowjobv1.PeriodicJob
				pj.Spec.Agent = prowjobv1.TektonAgent
				pj.Spec.PipelineRunSpec = &pipelineSpec
				pr := makePipelineGitResource(pj)
				p, err := makePipelineRun(pj, "21", pr)
				if err != nil {
					panic(err)
				}
				p.Status.SetCondition(&duckv1alpha1.Condition{
					Type:    duckv1alpha1.ConditionSucceeded,
					Status:  corev1.ConditionFalse,
					Message: "Pipeline jx/jenkins-x-jx-pr-5266-images-16 can't be found:pipeline.tekton.dev\n" +
						"\"jenkins-x-jx-pr-5266-images-16\" not found",
				})
				p.CreationTimestamp = metav1.Now()
				return []*pipelinev1alpha1.PipelineRun{p}
			}(),
			expectedJob: func(pj prowjobv1.ProwJob, _ pipelinev1alpha1.PipelineRun) prowjobv1.ProwJob {
				pj.Status = prowjobv1.ProwJobStatus{
					StartTime:      now,
					State:          prowjobv1.TriggeredState,
				}
				return pj
			},
			expectedPipelineRun: nil,
		},
		{
			name: "with successful metapipeline and failed pipeline with race condition",
			observedJob: &prowjobv1.ProwJob{
				Spec: prowjobv1.ProwJobSpec{
					Agent:           prowjobv1.TektonAgent,
					PipelineRunSpec: &pipelineSpec,
				},
				Status: prowjobv1.ProwJobStatus{
					State:       prowjobv1.PendingState,
					Description: "fancy",
				},
			},
			observedPipelineRuns: func() []*pipelinev1alpha1.PipelineRun {
				pj := prowjobv1.ProwJob{}
				pj.Spec.Type = prowjobv1.PeriodicJob
				pj.Spec.Agent = prowjobv1.TektonAgent
				pj.Spec.PipelineRunSpec = &pipelinev1alpha1.PipelineRunSpec{
					ServiceAccount: "robot",
				}
				pr := makePipelineGitResource(pj)
				metaP, err := makePipelineRunWithPrefix(pj, "5", pr, "meta")
				if err != nil {
					panic(err)
				}
				metaP.Status.SetCondition(&duckv1alpha1.Condition{
					Type:    duckv1alpha1.ConditionSucceeded,
					Status:  corev1.ConditionTrue,
					Reason:  "Succeeded",
					Message: "Metapipeline",
				})
				metaP.CreationTimestamp = metav1.Now()
				execP, err := makePipelineRun(pj, "5", pr)
				if err != nil {
					panic(err)
				}
				execP.Status.SetCondition(&duckv1alpha1.Condition{
					Type:    duckv1alpha1.ConditionSucceeded,
					Status:  corev1.ConditionFalse,
					Message: "Pipeline jx/jenkins-x-jx-pr-5266-images-16 can't be found:pipeline.tekton.dev\n" +
						"\"jenkins-x-jx-pr-5266-images-16\" not found",
				})
				execP.CreationTimestamp = metav1.Now()
				return []*pipelinev1alpha1.PipelineRun{metaP, execP}
			}(),
			expectedJob: func(pj prowjobv1.ProwJob, _ pipelinev1alpha1.PipelineRun) prowjobv1.ProwJob {
				pj.Status = prowjobv1.ProwJobStatus{
					StartTime:      now,
					State:          prowjobv1.TriggeredState,
				}
				return pj
			},
			expectedPipelineRun: nil,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			name := "the-object-name"
			// prowjobs all live in the same ns, so use name for injecting errors
			if tc.namespace == errorGetProwJob {
				name = errorGetProwJob
			} else if tc.namespace == errorUpdateProwJob {
				name = errorUpdateProwJob
			}

			r := &fakeReconciler{
				jobs:      map[string]prowjobv1.ProwJob{},
				pipelines: map[string]pipelinev1alpha1.PipelineRun{},
				nows:      now,
			}

			jk := toKey(fakePJCtx, fakePJNS, name, prowJob)
			if j := tc.observedJob; j != nil {
				j.Name = name
				j.Spec.Type = prowjobv1.PeriodicJob
				r.jobs[jk] = *j
			}
			pk := toKey(tc.context, tc.namespace, name, pipelineRun)
			metapk := toKey(tc.context, tc.namespace, "meta-"+name, pipelineRun)
			// If there's just one observed run, there's no metapipeline involved.
			if len(tc.observedPipelineRuns) == 1 {
				p := tc.observedPipelineRuns[0]
				p.Name = name
				p.Labels[kube.ProwJobIDLabel] = name
				r.pipelines[pk] = *p
			} else if len(tc.observedPipelineRuns) == 2 {
				// The first is going to be the meta pipeline, the second the executing pipeline
				mp := tc.observedPipelineRuns[0]
				mp.Name = "meta-" + name
				mp.Labels[kube.ProwJobIDLabel] = name
				r.pipelines[metapk] = *mp

				p := tc.observedPipelineRuns[1]
				p.Name = name
				p.Labels[kube.ProwJobIDLabel] = name
				r.pipelines[pk] = *p
			}

			expectedJobs := map[string]prowjobv1.ProwJob{}
			if j := tc.expectedJob; j != nil {
				expectedJobs[jk] = j(r.jobs[jk], r.pipelines[pk])
			}
			expectedPipelineRuns := map[string]pipelinev1alpha1.PipelineRun{}
			if p := tc.expectedPipelineRun; p != nil {
				expectedPipelineRuns[pk] = p(r.jobs[jk], r.pipelines[pk])
				if val, ok := r.pipelines[metapk]; ok {
					expectedPipelineRuns[metapk] = p(r.jobs[jk], val)
				}
			}

			tk := toKey(tc.context, tc.namespace, name, prowJob)
			err := reconcile(r, tk)
			switch {
			case err != nil:
				if !tc.err {
					t.Errorf("unexpected error: %v", err)
				}
			case tc.err:
				t.Error("failed to receive expected error")
			case !equality.Semantic.DeepEqual(r.jobs, expectedJobs):
				t.Errorf("prowjobs do not match:\n%s", diff.ObjectReflectDiff(expectedJobs, r.jobs))
			case !equality.Semantic.DeepEqual(r.pipelines, expectedPipelineRuns):
				t.Errorf("pipelineruns do not match:\n%s", diff.ObjectReflectDiff(expectedPipelineRuns, r.pipelines))
			}
		})
	}

}

func TestDefaultEnv(t *testing.T) {
	cases := []struct {
		name     string
		c        corev1.Container
		env      map[string]string
		expected corev1.Container
	}{
		{
			name: "nothing set works",
		},
		{
			name: "add env",
			env: map[string]string{
				"hello": "world",
			},
			expected: corev1.Container{
				Env: []corev1.EnvVar{{Name: "hello", Value: "world"}},
			},
		},
		{
			name: "do not override env",
			c: corev1.Container{
				Env: []corev1.EnvVar{
					{Name: "ignore", Value: "this"},
					{Name: "keep", Value: "original value"},
				},
			},
			env: map[string]string{
				"hello": "world",
				"keep":  "should not see this",
			},
			expected: corev1.Container{
				Env: []corev1.EnvVar{
					{Name: "ignore", Value: "this"},
					{Name: "keep", Value: "original value"},
					{Name: "hello", Value: "world"},
				},
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := tc.c
			defaultEnv(&c, tc.env)
			if !equality.Semantic.DeepEqual(c, tc.expected) {
				t.Errorf("pipelines do not match:\n%s", diff.ObjectReflectDiff(&tc.expected, c))
			}
		})
	}
}

func TestPipelineRunMeta(t *testing.T) {
	cases := []struct {
		name     string
		pj       prowjobv1.ProwJob
		expected func(prowjobv1.ProwJob, *metav1.ObjectMeta)
	}{
		{
			name: "Use pj.Spec.Namespace for pipeline namespace",
			pj: prowjobv1.ProwJob{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "whatever",
					Namespace: "wrong",
				},
				Spec: prowjobv1.ProwJobSpec{
					Namespace: "correct",
				},
			},
			expected: func(pj prowjobv1.ProwJob, meta *metav1.ObjectMeta) {
				meta.Name = pj.Name
				meta.Namespace = pj.Spec.Namespace
				meta.Labels, meta.Annotations = decorate.LabelsAndAnnotationsForJob(pj)
				meta.OwnerReferences = []metav1.OwnerReference{ownerRef(pj)}
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var expected metav1.ObjectMeta
			tc.expected(tc.pj, &expected)
			actual := pipelineMeta(tc.pj)
			if !equality.Semantic.DeepEqual(actual, expected) {
				t.Errorf("pipeline meta does not match:\n%s", diff.ObjectReflectDiff(expected, actual))
			}
		})
	}
}
func TestMakePipelineGitResouce(t *testing.T) {
	cases := []struct {
		name string
		job  func(prowjobv1.ProwJob) prowjobv1.ProwJob
		err  bool
	}{
		{
			name: "creates valid pipeline resource with emtpy paramaters",
		},
		{
			name: "creates valid pipeline resource with sourceURL and base SHA",
			job: func(pj prowjobv1.ProwJob) prowjobv1.ProwJob {
				pj.Spec.Refs = &prowjobv1.Refs{
					CloneURI: "https://github.com/test/test.git",
					BaseSHA:  "test",
				}
				return pj
			},
		},
		{
			name: "creates valid pipeline resource with sourceURL and pull request SHA",
			job: func(pj prowjobv1.ProwJob) prowjobv1.ProwJob {
				pj.Spec.Refs = &prowjobv1.Refs{
					CloneURI: "https://github.com/test/test.git",
					Pulls: []prowjobv1.Pull{
						{
							SHA: "test",
						},
					},
				}
				return pj
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			pj := prowjobv1.ProwJob{}
			pj.Name = "hello"
			pj.Namespace = "world"

			if tc.job != nil {
				pj = tc.job(pj)
			}

			actual := makePipelineGitResource(pj)

			refs := pj.Spec.Refs
			sourceURL := ""
			revision := ""
			if refs != nil {
				sourceURL = refs.CloneURI
				if len(refs.Pulls) > 0 {
					revision = refs.Pulls[0].SHA
				} else {
					revision = refs.BaseSHA
				}
			}
			expected := pipelinev1alpha1.PipelineResource{
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
					},
				},
			}

			if !equality.Semantic.DeepEqual(actual, &expected) {
				t.Errorf("pipelineresources do not match:\n%s", diff.ObjectReflectDiff(&expected, actual))
			}
		})
	}
}

func TestMakePipelineRun(t *testing.T) {
	cases := []struct {
		name string
		job  func(prowjobv1.ProwJob) prowjobv1.ProwJob
		err  bool
	}{
		{
			name: "reject empty prow job",
			job:  func(_ prowjobv1.ProwJob) prowjobv1.ProwJob { return prowjobv1.ProwJob{} },
			err:  true,
		},
		{
			name: "return valid pipeline with valid prowjob",
		},
		{
			name: "configure source when refs are set",
			job: func(pj prowjobv1.ProwJob) prowjobv1.ProwJob {
				pj.Spec.ExtraRefs = []prowjobv1.Refs{{Org: "bonus"}}
				pj.Spec.DecorationConfig = &prowjobv1.DecorationConfig{
					UtilityImages: &prowjobv1.UtilityImages{},
				}
				return pj
			},
		},
		{
			name: "do not override source when set",
			job: func(pj prowjobv1.ProwJob) prowjobv1.ProwJob {
				pj.Spec.ExtraRefs = []prowjobv1.Refs{{Org: "bonus"}}
				pj.Spec.DecorationConfig = &prowjobv1.DecorationConfig{
					UtilityImages: &prowjobv1.UtilityImages{},
				}

				return pj
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			pj := prowjobv1.ProwJob{}
			pj.Name = "world"
			pj.Namespace = "hello"
			pj.Spec.Type = prowjobv1.PeriodicJob
			pj.Spec.PipelineRunSpec = &pipelinev1alpha1.PipelineRunSpec{}

			if tc.job != nil {
				pj = tc.job(pj)
			}
			const randomPipelineRunID = "so-many-pipelines"
			pr := makePipelineGitResource(pj)
			actual, err := makePipelineRun(pj, randomPipelineRunID, pr)
			if err != nil {
				if !tc.err {
					t.Errorf("unexpected error: %v", err)
				}
				return
			} else if tc.err {
				t.Error("failed to receive expected error")
			}
			expected := pipelinev1alpha1.PipelineRun{
				ObjectMeta: pipelineMeta(pj),
				Spec:       *pj.Spec.PipelineRunSpec,
			}
			expected.Spec.Params = append(expected.Spec.Params, pipelinev1alpha1.Param{
				Name:  "build_id",
				Value: randomPipelineRunID,
			})
			rb := pipelinev1alpha1.PipelineResourceBinding{
				Name: pr.Name,
				ResourceRef: pipelinev1alpha1.PipelineResourceRef{
					Name:       pr.Name,
					APIVersion: pr.APIVersion,
				},
			}
			expected.Spec.Resources = append(expected.Spec.Resources, rb)

			if err != nil {
				t.Fatalf("failed to inject expected source: %v", err)
			}
			if !equality.Semantic.DeepEqual(actual, &expected) {
				t.Errorf("pipelineruns do not match:\n%s", diff.ObjectReflectDiff(&expected, actual))
			}
		})
	}
}

func TestDescription(t *testing.T) {
	cases := []struct {
		name     string
		message  string
		reason   string
		fallback string
		expected string
	}{
		{
			name:     "prefer message over reason or fallback",
			message:  "hello",
			reason:   "world",
			fallback: "doh",
			expected: "hello",
		},
		{
			name:     "prefer reason over fallback",
			reason:   "world",
			fallback: "other",
			expected: "world",
		},
		{
			name:     "use fallback if nothing else set",
			fallback: "fancy",
			expected: "fancy",
		},
	}

	for _, tc := range cases {
		bc := duckv1alpha1.Condition{
			Message: tc.message,
			Reason:  tc.reason,
		}
		if actual := description(bc, tc.fallback); actual != tc.expected {
			t.Errorf("%s: actual %q != expected %q", tc.name, actual, tc.expected)
		}
	}
}

func TestProwJobStatus(t *testing.T) {
	cases := []struct {
		name     string
		input    pipelinev1alpha1.PipelineRunStatus
		state    prowjobv1.ProwJobState
		desc     string
		fallback string
	}{
		{
			name:  "empty conditions returns triggered/scheduling",
			state: prowjobv1.TriggeredState,
			desc:  descScheduling,
		},
		{
			name: "truly succeeded state returns success",
			input: pipelinev1alpha1.PipelineRunStatus{
				Conditions: []duckv1alpha1.Condition{
					{
						Type:    duckv1alpha1.ConditionSucceeded,
						Status:  corev1.ConditionTrue,
						Message: "fancy",
					},
				},
			},
			state:    prowjobv1.SuccessState,
			desc:     "fancy",
			fallback: descSucceeded,
		},
		{
			name: "falsely succeeded state returns failure",
			input: pipelinev1alpha1.PipelineRunStatus{
				Conditions: []duckv1alpha1.Condition{
					{
						Type:    duckv1alpha1.ConditionSucceeded,
						Status:  corev1.ConditionFalse,
						Message: "weird",
					},
				},
			},
			state:    prowjobv1.FailureState,
			desc:     "weird",
			fallback: descFailed,
		},
		{
			name: "unstarted job returns pending/running",
			input: pipelinev1alpha1.PipelineRunStatus{
				Conditions: []duckv1alpha1.Condition{
					{
						Type:    duckv1alpha1.ConditionSucceeded,
						Status:  corev1.ConditionUnknown,
						Message: "hola",
					},
				},
			},
			state:    prowjobv1.PendingState,
			desc:     "hola",
			fallback: descRunning,
		},
		{
			name: "unfinished job returns running",
			input: pipelinev1alpha1.PipelineRunStatus{
				Conditions: []duckv1alpha1.Condition{
					{
						Type:    duckv1alpha1.ConditionSucceeded,
						Status:  corev1.ConditionUnknown,
						Message: "hola",
					},
				},
			},
			state:    prowjobv1.PendingState,
			desc:     "hola",
			fallback: descRunning,
		},
		{
			name: "pipelines with unknown success status are still running",
			input: pipelinev1alpha1.PipelineRunStatus{
				Conditions: []duckv1alpha1.Condition{
					{
						Type:    duckv1alpha1.ConditionSucceeded,
						Status:  corev1.ConditionUnknown,
						Message: "hola",
					},
				},
			},
			state:    prowjobv1.PendingState,
			desc:     "hola",
			fallback: descRunning,
		},
		{
			name:  "completed pipelines without a succeeded condition end in tirggered/scheduling",
			input: pipelinev1alpha1.PipelineRunStatus{},
			state: prowjobv1.TriggeredState,
			desc:  descScheduling,
		},
	}

	for _, tc := range cases {
		if len(tc.fallback) > 0 {
			tc.desc = tc.fallback
			tc.fallback = ""
			tc.name += " [fallback]"
			cond := tc.input.Conditions[0]
			cond.Message = ""
			tc.input.Conditions = []duckv1alpha1.Condition{cond}
			cases = append(cases, tc)
		}
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			state, desc := prowJobStatusForSinglePipelineRun(tc.input)
			if state != tc.state {
				t.Errorf("state %q != expected %q", state, tc.state)
			}
			if desc != tc.desc {
				t.Errorf("description %q != expected %q", desc, tc.desc)
			}
		})
	}
}
