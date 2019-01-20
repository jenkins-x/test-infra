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
	"errors"
	"fmt"
	"reflect"
	"testing"
	"time"

	pipelinev1alpha1 "github.com/knative/build-pipeline/pkg/apis/pipeline/v1alpha1"
	duckv1alpha1 "github.com/knative/pkg/apis/duck/v1alpha1"
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
	errorGetProwJob    = "error-get-prowjob"
	errorGetPipeline      = "error-get-pipeline"
	errorDeletePipeline   = "error-delete-pipeline"
	errorCreatePipeline   = "error-create-pipeline"
	errorUpdateProwJob = "error-update-prowjob"
)

type fakeReconciler struct {
	jobs   map[string]prowjobv1.ProwJob
	pipelines map[string]pipelinev1alpha1.Pipeline
	nows   metav1.Time
}

func (r *fakeReconciler) now() metav1.Time {
	fmt.Println(r.nows)
	return r.nows
}

const fakePJCtx = "prow-context"
const fakePJNS = "prow-job"

func (r *fakeReconciler) getProwJob(name string) (*prowjobv1.ProwJob, error) {
	if name == errorGetProwJob {
		return nil, errors.New("injected get prowjob error")
	}
	k := toKey(fakePJCtx, fakePJNS, name)
	pj, present := r.jobs[k]
	if !present {
		return nil, apierrors.NewNotFound(prowjobv1.Resource("ProwJob"), name)
	}
	return &pj, nil
}

func (r *fakeReconciler) updateProwJob(pj *prowjobv1.ProwJob) (*prowjobv1.ProwJob, error) {
	if pj.Name == errorUpdateProwJob {
		return nil, errors.New("injected update prowjob error")
	}
	if pj == nil {
		return nil, errors.New("nil prowjob")
	}
	k := toKey(fakePJCtx, fakePJNS, pj.Name)
	if _, present := r.jobs[k]; !present {
		return nil, apierrors.NewNotFound(prowjobv1.Resource("ProwJob"), pj.Name)
	}
	r.jobs[k] = *pj
	return pj, nil
}

func (r *fakeReconciler) getPipeline(context, namespace, name string) (*pipelinev1alpha1.Pipeline, error) {
	if namespace == errorGetPipeline {
		return nil, errors.New("injected create pipeline error")
	}
	k := toKey(context, namespace, name)
	b, present := r.pipelines[k]
	if !present {
		return nil, apierrors.NewNotFound(pipelinev1alpha1.Resource("Pipeline"), name)
	}
	return &b, nil
}
func (r *fakeReconciler) deletePipeline(context, namespace, name string) error {
	if namespace == errorDeletePipeline {
		return errors.New("injected create pipeline error")
	}
	k := toKey(context, namespace, name)
	if _, present := r.pipelines[k]; !present {
		return apierrors.NewNotFound(pipelinev1alpha1.Resource("Pipeline"), name)
	}
	delete(r.pipelines, k)
	return nil
}

func (r *fakeReconciler) createPipeline(context, namespace string, b *pipelinev1alpha1.Pipeline) (*pipelinev1alpha1.Pipeline, error) {
	if b == nil {
		return nil, errors.New("nil pipeline")
	}
	if namespace == errorCreatePipeline {
		return nil, errors.New("injected create pipeline error")
	}
	k := toKey(context, namespace, b.Name)
	if _, alreadyExists := r.pipelines[k]; alreadyExists {
		return nil, apierrors.NewAlreadyExists(prowjobv1.Resource("ProwJob"), b.Name)
	}
	r.pipelines[k] = *b
	return b, nil
}

func (r *fakeReconciler) pipelineID(pj prowjobv1.ProwJob) (string, error) {
	return "7777777777", nil
}

type fakeLimiter struct {
	added string
}

func (fl *fakeLimiter) ShutDown() {}
func (fl *fakeLimiter) Get() (interface{}, bool) {
	return "not implemented", true
}
func (fl *fakeLimiter) Done(interface{})   {}
func (fl *fakeLimiter) Forget(interface{}) {}
func (fl *fakeLimiter) AddRateLimited(a interface{}) {
	fl.added = a.(string)
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
			obj: &pipelinev1alpha1.Pipeline{
				ObjectMeta: metav1.ObjectMeta{
					Namespace: "foo",
					Name:      "bar",
				},
			},
			expected: toKey("hey", "foo", "bar"),
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
			expected: toKey("rolo", "tomassi", "dude"),
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
	now := metav1.Now()
	pipelineSpec := pipelinev1alpha1.PipelineSpec{}
	noJobChange := func(pj prowjobv1.ProwJob, _ pipelinev1alpha1.Pipeline) prowjobv1.ProwJob {
		return pj
	}
	noPipelineChange := func(_ prowjobv1.ProwJob, b pipelinev1alpha1.Pipeline) pipelinev1alpha1.Pipeline {
		return b
	}
	cases := []struct {
		name          string
		namespace     string
		context       string
		observedJob   *prowjobv1.ProwJob
		observedPipeline *pipelinev1alpha1.Pipeline
		expectedJob   func(prowjobv1.ProwJob, pipelinev1alpha1.Pipeline) prowjobv1.ProwJob
		expectedPipeline func(prowjobv1.ProwJob, pipelinev1alpha1.Pipeline) pipelinev1alpha1.Pipeline
		err           bool
	}{{
		name: "new prow job creates pipeline",
		observedJob: &prowjobv1.ProwJob{
			Spec: prowjobv1.ProwJobSpec{
				Agent:     prowjobv1.KnativePipelineAgent,
				PipelineSpec: &pipelineSpec,
			},
		},
		expectedJob: func(pj prowjobv1.ProwJob, _ pipelinev1alpha1.Pipeline) prowjobv1.ProwJob {
			pj.Status = prowjobv1.ProwJobStatus{
				StartTime:   now,
				State:       prowjobv1.TriggeredState,
				Description: descScheduling,
			}
			return pj
		},
		expectedPipeline: func(pj prowjobv1.ProwJob, _ pipelinev1alpha1.Pipeline) pipelinev1alpha1.Pipeline {
			pj.Spec.Type = prowjobv1.PeriodicJob
			b, err := makePipeline(pj, "50")
			if err != nil {
				panic(err)
			}
			return *b
		},
	},
		{
			name: "do not create pipeline for failed prowjob",
			observedJob: &prowjobv1.ProwJob{
				Spec: prowjobv1.ProwJobSpec{
					Agent:     prowjobv1.KnativePipelineAgent,
					PipelineSpec: &pipelineSpec,
				},
				Status: prowjobv1.ProwJobStatus{
					State: prowjobv1.FailureState,
				},
			},
			expectedJob: noJobChange,
		},
		{
			name: "do not create pipeline for successful prowjob",
			observedJob: &prowjobv1.ProwJob{
				Spec: prowjobv1.ProwJobSpec{
					Agent:     prowjobv1.KnativePipelineAgent,
					PipelineSpec: &pipelineSpec,
				},
				Status: prowjobv1.ProwJobStatus{
					State: prowjobv1.SuccessState,
				},
			},
			expectedJob: noJobChange,
		},
		{
			name: "do not create pipeline for aborted prowjob",
			observedJob: &prowjobv1.ProwJob{
				Spec: prowjobv1.ProwJobSpec{
					Agent:     prowjobv1.KnativePipelineAgent,
					PipelineSpec: &pipelineSpec,
				},
				Status: prowjobv1.ProwJobStatus{
					State: prowjobv1.AbortedState,
				},
			},
			expectedJob: noJobChange,
		},
		{
			name: "delete pipeline after deleting prowjob",
			observedPipeline: func() *pipelinev1alpha1.Pipeline {
				pj := prowjobv1.ProwJob{}
				pj.Spec.Type = prowjobv1.PeriodicJob
				pj.Spec.PipelineSpec = &pipelinev1alpha1.PipelineSpec{}
				b, err := makePipeline(pj, "7")
				if err != nil {
					panic(err)
				}
				return b
			}(),
		},
		{
			name: "do not delete deleted pipelines",
			observedPipeline: func() *pipelinev1alpha1.Pipeline {
				pj := prowjobv1.ProwJob{}
				pj.Spec.Type = prowjobv1.PeriodicJob
				pj.Spec.PipelineSpec = &pipelinev1alpha1.PipelineSpec{}
				b, err := makePipeline(pj, "6")
				b.DeletionTimestamp = &now
				if err != nil {
					panic(err)
				}
				return b
			}(),
			expectedPipeline: noPipelineChange,
		},
		{
			name: "only delete pipelines created by controller",
			observedPipeline: func() *pipelinev1alpha1.Pipeline {
				pj := prowjobv1.ProwJob{}
				pj.Spec.Type = prowjobv1.PeriodicJob
				pj.Spec.PipelineSpec = &pipelinev1alpha1.PipelineSpec{}
				b, err := makePipeline(pj, "9999")
				if err != nil {
					panic(err)
				}
				delete(b.Labels, kube.CreatedByProw)
				return b
			}(),
			expectedPipeline: noPipelineChange,
		},
		{
			name:    "delete prow pipelines in the wrong cluster",
			context: "wrong-cluster",
			observedJob: &prowjobv1.ProwJob{
				Spec: prowjobv1.ProwJobSpec{
					Agent:   prowjobv1.KnativePipelineAgent,
					Cluster: "target-cluster",
					PipelineSpec: &pipelinev1alpha1.PipelineSpec{
						ServiceAccountName: "robot",
					},
				},
				Status: prowjobv1.ProwJobStatus{
					State:       prowjobv1.PendingState,
					StartTime:   metav1.Now(),
					Description: "fancy",
				},
			},
			observedPipeline: func() *pipelinev1alpha1.Pipeline {
				pj := prowjobv1.ProwJob{}
				pj.Spec.Type = prowjobv1.PeriodicJob
				pj.Spec.Agent = prowjobv1.KnativePipelineAgent
				pj.Spec.PipelineSpec = &pipelineSpec
				b, err := makePipeline(pj, "5")
				if err != nil {
					panic(err)
				}
				return b
			}(),
			expectedJob: noJobChange,
		},
		{
			name:    "ignore random pipelines in the wrong cluster",
			context: "wrong-cluster",
			observedJob: &prowjobv1.ProwJob{
				Spec: prowjobv1.ProwJobSpec{
					Agent:   prowjobv1.KnativePipelineAgent,
					Cluster: "target-cluster",
					PipelineSpec: &pipelinev1alpha1.PipelineSpec{
						ServiceAccountName: "robot",
					},
				},
				Status: prowjobv1.ProwJobStatus{
					State:       prowjobv1.PendingState,
					StartTime:   metav1.Now(),
					Description: "fancy",
				},
			},
			observedPipeline: func() *pipelinev1alpha1.Pipeline {
				pj := prowjobv1.ProwJob{}
				pj.Spec.Type = prowjobv1.PeriodicJob
				pj.Spec.Agent = prowjobv1.KnativePipelineAgent
				pj.Spec.PipelineSpec = &pipelineSpec
				b, err := makePipeline(pj, "5")
				if err != nil {
					panic(err)
				}
				delete(b.Labels, kube.CreatedByProw)
				return b
			}(),
			expectedJob:   noJobChange,
			expectedPipeline: noPipelineChange,
		},
		{
			name: "update job status if pipeline resets",
			observedJob: &prowjobv1.ProwJob{
				Spec: prowjobv1.ProwJobSpec{
					Agent: prowjobv1.KnativePipelineAgent,
					PipelineSpec: &pipelinev1alpha1.PipelineSpec{
						ServiceAccountName: "robot",
					},
				},
				Status: prowjobv1.ProwJobStatus{
					State:       prowjobv1.PendingState,
					StartTime:   metav1.Now(),
					Description: "fancy",
				},
			},
			observedPipeline: func() *pipelinev1alpha1.Pipeline {
				pj := prowjobv1.ProwJob{}
				pj.Spec.Type = prowjobv1.PeriodicJob
				pj.Spec.Agent = prowjobv1.KnativePipelineAgent
				pj.Spec.PipelineSpec = &pipelineSpec
				b, err := makePipeline(pj, "5")
				if err != nil {
					panic(err)
				}
				return b
			}(),
			expectedJob: func(pj prowjobv1.ProwJob, _ pipelinev1alpha1.Pipeline) prowjobv1.ProwJob {
				pj.Status.State = prowjobv1.TriggeredState
				pj.Status.Description = descScheduling
				return pj
			},
			expectedPipeline: noPipelineChange,
		},
		{
			name: "prowjob goes pending when pipeline starts",
			observedJob: &prowjobv1.ProwJob{
				Spec: prowjobv1.ProwJobSpec{
					Agent:     prowjobv1.KnativePipelineAgent,
					PipelineSpec: &pipelineSpec,
				},
				Status: prowjobv1.ProwJobStatus{
					State:       prowjobv1.TriggeredState,
					Description: "fancy",
				},
			},
			observedPipeline: func() *pipelinev1alpha1.Pipeline {
				pj := prowjobv1.ProwJob{}
				pj.Spec.Type = prowjobv1.PeriodicJob
				pj.Spec.Agent = prowjobv1.KnativePipelineAgent
				pj.Spec.PipelineSpec = &pipelineSpec
				b, err := makePipeline(pj, "1")
				if err != nil {
					panic(err)
				}
				b.Status.SetCondition(&duckv1alpha1.Condition{
					Type:    pipelinev1alpha1.PipelineSucceeded,
					Message: "hello",
				})
				b.Status.StartTime = now
				return b
			}(),
			expectedJob: func(pj prowjobv1.ProwJob, _ pipelinev1alpha1.Pipeline) prowjobv1.ProwJob {
				pj.Status = prowjobv1.ProwJobStatus{
					StartTime:   now,
					State:       prowjobv1.PendingState,
					Description: "hello",
				}
				return pj
			},
			expectedPipeline: noPipelineChange,
		},
		{
			name: "prowjob succeeds when pipeline succeeds",
			observedJob: &prowjobv1.ProwJob{
				Spec: prowjobv1.ProwJobSpec{
					Agent:     prowjobv1.KnativePipelineAgent,
					PipelineSpec: &pipelineSpec,
				},
				Status: prowjobv1.ProwJobStatus{
					State:       prowjobv1.PendingState,
					Description: "fancy",
				},
			},
			observedPipeline: func() *pipelinev1alpha1.Pipeline {
				pj := prowjobv1.ProwJob{}
				pj.Spec.Type = prowjobv1.PeriodicJob
				pj.Spec.Agent = prowjobv1.KnativePipelineAgent
				pj.Spec.PipelineSpec = &pipelineSpec
				b, err := makePipeline(pj, "22")
				if err != nil {
					panic(err)
				}
				b.Status.SetCondition(&duckv1alpha1.Condition{
					Type:    pipelinev1alpha1.PipelineSucceeded,
					Status:  corev1.ConditionTrue,
					Message: "hello",
				})
				b.Status.CompletionTime = now
				b.Status.StartTime = now
				return b
			}(),
			expectedJob: func(pj prowjobv1.ProwJob, _ pipelinev1alpha1.Pipeline) prowjobv1.ProwJob {
				pj.Status = prowjobv1.ProwJobStatus{
					StartTime:      now,
					CompletionTime: &now,
					State:          prowjobv1.SuccessState,
					Description:    "hello",
				}
				return pj
			},
			expectedPipeline: noPipelineChange,
		},
		{
			name: "prowjob fails when pipeline fails",
			observedJob: &prowjobv1.ProwJob{
				Spec: prowjobv1.ProwJobSpec{
					Agent:     prowjobv1.KnativePipelineAgent,
					PipelineSpec: &pipelineSpec,
				},
				Status: prowjobv1.ProwJobStatus{
					State:       prowjobv1.PendingState,
					Description: "fancy",
				},
			},
			observedPipeline: func() *pipelinev1alpha1.Pipeline {
				pj := prowjobv1.ProwJob{}
				pj.Spec.Type = prowjobv1.PeriodicJob
				pj.Spec.Agent = prowjobv1.KnativePipelineAgent
				pj.Spec.PipelineSpec = &pipelineSpec
				b, err := makePipeline(pj, "21")
				if err != nil {
					panic(err)
				}
				b.Status.SetCondition(&duckv1alpha1.Condition{
					Type:    pipelinev1alpha1.PipelineSucceeded,
					Status:  corev1.ConditionFalse,
					Message: "hello",
				})
				b.Status.StartTime = now
				b.Status.CompletionTime = now
				return b
			}(),
			expectedJob: func(pj prowjobv1.ProwJob, _ pipelinev1alpha1.Pipeline) prowjobv1.ProwJob {
				pj.Status = prowjobv1.ProwJobStatus{
					StartTime:      now,
					CompletionTime: &now,
					State:          prowjobv1.FailureState,
					Description:    "hello",
				}
				return pj
			},
			expectedPipeline: noPipelineChange,
		},
		{
			name:      "error when we cannot get prowjob",
			namespace: errorGetProwJob,
			err:       true,
			observedJob: &prowjobv1.ProwJob{
				Spec: prowjobv1.ProwJobSpec{
					Agent:     prowjobv1.KnativePipelineAgent,
					PipelineSpec: &pipelineSpec,
				},
				Status: prowjobv1.ProwJobStatus{
					State:       prowjobv1.PendingState,
					Description: "fancy",
				},
			},
		},
		{
			name:      "error when we cannot get pipeline",
			namespace: errorGetPipeline,
			err:       true,
			observedPipeline: func() *pipelinev1alpha1.Pipeline {
				pj := prowjobv1.ProwJob{}
				pj.Spec.Type = prowjobv1.PeriodicJob
				pj.Spec.Agent = prowjobv1.KnativePipelineAgent
				pj.Spec.PipelineSpec = &pipelineSpec
				b, err := makePipeline(pj, "-72")
				if err != nil {
					panic(err)
				}
				b.Status.SetCondition(&duckv1alpha1.Condition{
					Type:    pipelinev1alpha1.PipelineSucceeded,
					Status:  corev1.ConditionTrue,
					Message: "hello",
				})
				b.Status.CompletionTime = now
				b.Status.StartTime = now
				return b
			}(),
		},
		{
			name:      "error when we cannot delete pipeline",
			namespace: errorDeletePipeline,
			err:       true,
			observedPipeline: func() *pipelinev1alpha1.Pipeline {
				pj := prowjobv1.ProwJob{}
				pj.Spec.Type = prowjobv1.PeriodicJob
				pj.Spec.PipelineSpec = &pipelinev1alpha1.PipelineSpec{}
				b, err := makePipeline(pj, "44")
				if err != nil {
					panic(err)
				}
				return b
			}(),
		},
		{
			name:      "error when we cannot create pipeline",
			namespace: errorCreatePipeline,
			err:       true,
			observedJob: &prowjobv1.ProwJob{
				Spec: prowjobv1.ProwJobSpec{
					Agent:     prowjobv1.KnativePipelineAgent,
					PipelineSpec: &pipelineSpec,
				},
			},
			expectedJob: func(pj prowjobv1.ProwJob, _ pipelinev1alpha1.Pipeline) prowjobv1.ProwJob {
				pj.Status = prowjobv1.ProwJobStatus{
					StartTime:   now,
					State:       prowjobv1.TriggeredState,
					Description: descScheduling,
				}
				return pj
			},
		},
		{
			name: "error when pipelinespec is nil",
			err:  true,
			observedJob: &prowjobv1.ProwJob{
				Spec: prowjobv1.ProwJobSpec{
					Agent:     prowjobv1.KnativePipelineAgent,
					PipelineSpec: nil,
				},
				Status: prowjobv1.ProwJobStatus{
					State: prowjobv1.TriggeredState,
				},
			},
		},
		{
			name:      "error when we cannot update prowjob",
			namespace: errorUpdateProwJob,
			err:       true,
			observedJob: &prowjobv1.ProwJob{
				Spec: prowjobv1.ProwJobSpec{
					Agent:     prowjobv1.KnativePipelineAgent,
					PipelineSpec: &pipelineSpec,
				},
				Status: prowjobv1.ProwJobStatus{
					State:       prowjobv1.PendingState,
					Description: "fancy",
				},
			},
			observedPipeline: func() *pipelinev1alpha1.Pipeline {
				pj := prowjobv1.ProwJob{}
				pj.Spec.Type = prowjobv1.PeriodicJob
				pj.Spec.Agent = prowjobv1.KnativePipelineAgent
				pj.Spec.PipelineSpec = &pipelineSpec
				b, err := makePipeline(pj, "42")
				if err != nil {
					panic(err)
				}
				b.Status.SetCondition(&duckv1alpha1.Condition{
					Type:    pipelinev1alpha1.PipelineSucceeded,
					Status:  corev1.ConditionTrue,
					Message: "hello",
				})
				b.Status.CompletionTime = now
				b.Status.StartTime = now
				return b
			}(),
		}}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			name := "the-object-name"
			// prowjobs all live in the same ns, so use name for injecting errors
			if tc.namespace == errorGetProwJob {
				name = errorGetProwJob
			} else if tc.namespace == errorUpdateProwJob {
				name = errorUpdateProwJob
			}
			bk := toKey(tc.context, tc.namespace, name)
			jk := toKey(fakePJCtx, fakePJNS, name)
			r := &fakeReconciler{
				jobs:   map[string]prowjobv1.ProwJob{},
				pipelines: map[string]pipelinev1alpha1.Pipeline{},
				nows:   now,
			}
			if j := tc.observedJob; j != nil {
				j.Name = name
				j.Spec.Type = prowjobv1.PeriodicJob
				r.jobs[jk] = *j
			}
			if b := tc.observedPipeline; b != nil {
				b.Name = name
				r.pipelines[bk] = *b
			}
			expectedJobs := map[string]prowjobv1.ProwJob{}
			if j := tc.expectedJob; j != nil {
				expectedJobs[jk] = j(r.jobs[jk], r.pipelines[bk])
			}
			expectedPipelines := map[string]pipelinev1alpha1.Pipeline{}
			if b := tc.expectedPipeline; b != nil {
				expectedPipelines[bk] = b(r.jobs[jk], r.pipelines[bk])
			}
			err := reconcile(r, bk)
			switch {
			case err != nil:
				if !tc.err {
					t.Errorf("unexpected error: %v", err)
				}
			case tc.err:
				t.Error("failed to receive expected error")
			case !equality.Semantic.DeepEqual(r.jobs, expectedJobs):
				t.Errorf("prowjobs do not match:\n%s", diff.ObjectReflectDiff(expectedJobs, r.jobs))
			case !equality.Semantic.DeepEqual(r.pipelines, expectedPipelines):
				t.Errorf("pipelines do not match:\n%s", diff.ObjectReflectDiff(expectedPipelines, r.pipelines))
			}
		})
	}

}

func TestDefaultArguments(t *testing.T) {
	cases := []struct {
		name     string
		t        pipelinev1alpha1.TemplateInstantiationSpec
		env      map[string]string
		expected pipelinev1alpha1.TemplateInstantiationSpec
	}{
		{
			name: "nothing set works",
		},
		{
			name: "add env",
			env: map[string]string{
				"hello": "world",
			},
			expected: pipelinev1alpha1.TemplateInstantiationSpec{
				Arguments: []pipelinev1alpha1.ArgumentSpec{{Name: "hello", Value: "world"}},
			},
		},
		{
			name: "do not override env",
			t: pipelinev1alpha1.TemplateInstantiationSpec{
				Arguments: []pipelinev1alpha1.ArgumentSpec{
					{Name: "ignore", Value: "this"},
					{Name: "keep", Value: "original value"},
				},
			},
			env: map[string]string{
				"hello": "world",
				"keep":  "should not see this",
			},
			expected: pipelinev1alpha1.TemplateInstantiationSpec{
				Arguments: []pipelinev1alpha1.ArgumentSpec{
					{Name: "ignore", Value: "this"},
					{Name: "keep", Value: "original value"},
					{Name: "hello", Value: "world"},
				},
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			templ := tc.t
			defaultArguments(&templ, tc.env)
			if !equality.Semantic.DeepEqual(templ, tc.expected) {
				t.Errorf("pipelines do not match:\n%s", diff.ObjectReflectDiff(&tc.expected, templ))
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

func TestInjectSource(t *testing.T) {
	cases := []struct {
		name     string
		pipeline    pipelinev1alpha1.Pipeline
		pj       prowjobv1.ProwJob
		expected func(*pipelinev1alpha1.Pipeline, prowjobv1.ProwJob)
		err      bool
	}{
		{
			name: "do nothing when source is set",
			pipeline: pipelinev1alpha1.Pipeline{
				Spec: pipelinev1alpha1.PipelineSpec{
					Source: &pipelinev1alpha1.SourceSpec{},
				},
			},
		},
		{
			name: "do nothing when no refs are set",
			pj: prowjobv1.ProwJob{
				Spec: prowjobv1.ProwJobSpec{
					Type: prowjobv1.PeriodicJob,
				},
			},
		},
		{
			name: "inject source, volumes, workdir when refs are set",
			pipeline: pipelinev1alpha1.Pipeline{
				Spec: pipelinev1alpha1.PipelineSpec{
					Steps: []corev1.Container{
						{}, // Override
						{WorkingDir: "do not override"},
					},
					Template: &pipelinev1alpha1.TemplateInstantiationSpec{},
				},
			},
			pj: prowjobv1.ProwJob{
				Spec: prowjobv1.ProwJobSpec{
					ExtraRefs: []prowjobv1.Refs{{Org: "hi", Repo: "there"}},
					DecorationConfig: &prowjobv1.DecorationConfig{
						UtilityImages: &prowjobv1.UtilityImages{},
					},
				},
			},
			expected: func(b *pipelinev1alpha1.Pipeline, pj prowjobv1.ProwJob) {
				src, _, cloneVolumes, err := decorate.CloneRefs(pj, codeMount, logMount)
				if err != nil {
					t.Fatalf("failed to make clonerefs container: %v", err)
				}
				src.Name = ""
				b.Spec.Volumes = append(b.Spec.Volumes, cloneVolumes...)
				b.Spec.Source = &pipelinev1alpha1.SourceSpec{
					Custom: src,
				}
				wd := workDir(pj.Spec.ExtraRefs[0])
				b.Spec.Template.Arguments = append(b.Spec.Template.Arguments, wd)
				b.Spec.Steps[0].WorkingDir = wd.Value

			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			expected := tc.pipeline
			if tc.expected != nil {
				tc.expected(&expected, tc.pj)
			}

			actual := &tc.pipeline
			err := injectSource(actual, tc.pj)
			switch {
			case err != nil:
				if !tc.err {
					t.Errorf("unexpected error: %v", err)
				}
			case tc.err:
				t.Error("failed to return expected error")
			case !equality.Semantic.DeepEqual(actual, &expected):
				t.Errorf("pipelines do not match:\n%s", diff.ObjectReflectDiff(&expected, actual))
			}
		})
	}
}

func TestPipelineMeta(t *testing.T) {
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

func TestMakePipeline(t *testing.T) {
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
				pj.Spec.PipelineSpec.Source = &pipelinev1alpha1.SourceSpec{}
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
			pj.Spec.PipelineSpec = &pipelinev1alpha1.PipelineSpec{}
			pj.Spec.PipelineSpec.Steps = append(pj.Spec.PipelineSpec.Steps, corev1.Container{})
			pj.Spec.PipelineSpec.Template = &pipelinev1alpha1.TemplateInstantiationSpec{}
			if tc.job != nil {
				pj = tc.job(pj)
			}
			const randomPipelineID = "so-many-pipelines"
			actual, err := makePipeline(pj, randomPipelineID)
			if err != nil {
				if !tc.err {
					t.Errorf("unexpected error: %v", err)
				}
				return
			} else if tc.err {
				t.Error("failed to receive expected error")
			}
			expected := pipelinev1alpha1.Pipeline{
				ObjectMeta: pipelineMeta(pj),
				Spec:       *pj.Spec.PipelineSpec,
			}
			env, err := pipelineEnv(pj, randomPipelineID)
			if err != nil {
				t.Fatalf("failed to create expected pipeline env: %v", err)
			}
			injectEnvironment(&expected, env)
			err = injectSource(&expected, pj)
			if err != nil {
				t.Fatalf("failed to inject expected source: %v", err)
			}
			if !equality.Semantic.DeepEqual(actual, &expected) {
				t.Errorf("pipelines do not match:\n%s", diff.ObjectReflectDiff(&expected, actual))
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
	now := metav1.Now()
	later := metav1.NewTime(now.Time.Add(1 * time.Hour))
	cases := []struct {
		name     string
		input    pipelinev1alpha1.PipelineStatus
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
			input: pipelinev1alpha1.PipelineStatus{
				Conditions: []duckv1alpha1.Condition{
					{
						Type:    pipelinev1alpha1.PipelineSucceeded,
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
			input: pipelinev1alpha1.PipelineStatus{
				Conditions: []duckv1alpha1.Condition{
					{
						Type:    pipelinev1alpha1.PipelineSucceeded,
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
			name: "unstarted job returns triggered/initializing",
			input: pipelinev1alpha1.PipelineStatus{
				Conditions: []duckv1alpha1.Condition{
					{
						Type:    pipelinev1alpha1.PipelineSucceeded,
						Status:  corev1.ConditionUnknown,
						Message: "hola",
					},
				},
			},
			state:    prowjobv1.TriggeredState,
			desc:     "hola",
			fallback: descInitializing,
		},
		{
			name: "unfinished job returns running",
			input: pipelinev1alpha1.PipelineStatus{
				StartTime: now,
				Conditions: []duckv1alpha1.Condition{
					{
						Type:    pipelinev1alpha1.PipelineSucceeded,
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
			input: pipelinev1alpha1.PipelineStatus{
				StartTime:      now,
				CompletionTime: later,
				Conditions: []duckv1alpha1.Condition{
					{
						Type:    pipelinev1alpha1.PipelineSucceeded,
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
			name: "completed pipelines without a succeeded condition end in error",
			input: pipelinev1alpha1.PipelineStatus{
				StartTime:      now,
				CompletionTime: later,
			},
			state: prowjobv1.ErrorState,
			desc:  descMissingCondition,
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
			state, desc := prowJobStatus(tc.input)
			if state != tc.state {
				t.Errorf("state %q != expected %q", state, tc.state)
			}
			if desc != tc.desc {
				t.Errorf("description %q != expected %q", desc, tc.desc)
			}
		})
	}
}
