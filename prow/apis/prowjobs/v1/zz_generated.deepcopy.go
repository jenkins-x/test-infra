// +build !ignore_autogenerated

/*
Copyright The Kubernetes Authors.

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

// Code generated by deepcopy-gen. DO NOT EDIT.

package v1

import (
	pipelinev1alpha1 "github.com/tektoncd/pipeline/pkg/apis/pipeline/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	runtime "k8s.io/apimachinery/pkg/runtime"
)

// DeepCopyInto is an autogenerated deepcopy function, copying the receiver, writing into out. in must be non-nil.
func (in *DecorationConfig) DeepCopyInto(out *DecorationConfig) {
	*out = *in
	if in.UtilityImages != nil {
		in, out := &in.UtilityImages, &out.UtilityImages
		*out = new(UtilityImages)
		**out = **in
	}
	if in.GCSConfiguration != nil {
		in, out := &in.GCSConfiguration, &out.GCSConfiguration
		*out = new(GCSConfiguration)
		**out = **in
	}
	if in.SSHKeySecrets != nil {
		in, out := &in.SSHKeySecrets, &out.SSHKeySecrets
		*out = make([]string, len(*in))
		copy(*out, *in)
	}
	if in.SSHHostFingerprints != nil {
		in, out := &in.SSHHostFingerprints, &out.SSHHostFingerprints
		*out = make([]string, len(*in))
		copy(*out, *in)
	}
	if in.SkipCloning != nil {
		in, out := &in.SkipCloning, &out.SkipCloning
		*out = new(bool)
		**out = **in
	}
	return
}

// DeepCopy is an autogenerated deepcopy function, copying the receiver, creating a new DecorationConfig.
func (in *DecorationConfig) DeepCopy() *DecorationConfig {
	if in == nil {
		return nil
	}
	out := new(DecorationConfig)
	in.DeepCopyInto(out)
	return out
}

// DeepCopyInto is an autogenerated deepcopy function, copying the receiver, writing into out. in must be non-nil.
func (in *GCSConfiguration) DeepCopyInto(out *GCSConfiguration) {
	*out = *in
	return
}

// DeepCopy is an autogenerated deepcopy function, copying the receiver, creating a new GCSConfiguration.
func (in *GCSConfiguration) DeepCopy() *GCSConfiguration {
	if in == nil {
		return nil
	}
	out := new(GCSConfiguration)
	in.DeepCopyInto(out)
	return out
}

// DeepCopyInto is an autogenerated deepcopy function, copying the receiver, writing into out. in must be non-nil.
func (in *ProwJob) DeepCopyInto(out *ProwJob) {
	*out = *in
	out.TypeMeta = in.TypeMeta
	in.ObjectMeta.DeepCopyInto(&out.ObjectMeta)
	in.Spec.DeepCopyInto(&out.Spec)
	in.Status.DeepCopyInto(&out.Status)
	return
}

// DeepCopy is an autogenerated deepcopy function, copying the receiver, creating a new ProwJob.
func (in *ProwJob) DeepCopy() *ProwJob {
	if in == nil {
		return nil
	}
	out := new(ProwJob)
	in.DeepCopyInto(out)
	return out
}

// DeepCopyObject is an autogenerated deepcopy function, copying the receiver, creating a new runtime.Object.
func (in *ProwJob) DeepCopyObject() runtime.Object {
	if c := in.DeepCopy(); c != nil {
		return c
	}
	return nil
}

// DeepCopyInto is an autogenerated deepcopy function, copying the receiver, writing into out. in must be non-nil.
func (in *ProwJobList) DeepCopyInto(out *ProwJobList) {
	*out = *in
	out.TypeMeta = in.TypeMeta
	out.ListMeta = in.ListMeta
	if in.Items != nil {
		in, out := &in.Items, &out.Items
		*out = make([]ProwJob, len(*in))
		for i := range *in {
			(*in)[i].DeepCopyInto(&(*out)[i])
		}
	}
	return
}

// DeepCopy is an autogenerated deepcopy function, copying the receiver, creating a new ProwJobList.
func (in *ProwJobList) DeepCopy() *ProwJobList {
	if in == nil {
		return nil
	}
	out := new(ProwJobList)
	in.DeepCopyInto(out)
	return out
}

// DeepCopyObject is an autogenerated deepcopy function, copying the receiver, creating a new runtime.Object.
func (in *ProwJobList) DeepCopyObject() runtime.Object {
	if c := in.DeepCopy(); c != nil {
		return c
	}
	return nil
}

// DeepCopyInto is an autogenerated deepcopy function, copying the receiver, writing into out. in must be non-nil.
func (in *ProwJobSpec) DeepCopyInto(out *ProwJobSpec) {
	*out = *in
	if in.Refs != nil {
		in, out := &in.Refs, &out.Refs
		*out = new(Refs)
		(*in).DeepCopyInto(*out)
	}
	if in.ExtraRefs != nil {
		in, out := &in.ExtraRefs, &out.ExtraRefs
		*out = make([]Refs, len(*in))
		for i := range *in {
			(*in)[i].DeepCopyInto(&(*out)[i])
		}
	}
	if in.PodSpec != nil {
		in, out := &in.PodSpec, &out.PodSpec
		*out = new(corev1.PodSpec)
		(*in).DeepCopyInto(*out)
	}
	if in.PipelineRunSpec != nil {
		in, out := &in.PipelineRunSpec, &out.PipelineRunSpec
		*out = new(pipelinev1alpha1.PipelineRunSpec)
		(*in).DeepCopyInto(*out)
	}
	if in.DecorationConfig != nil {
		in, out := &in.DecorationConfig, &out.DecorationConfig
		*out = new(DecorationConfig)
		(*in).DeepCopyInto(*out)
	}
	return
}

// DeepCopy is an autogenerated deepcopy function, copying the receiver, creating a new ProwJobSpec.
func (in *ProwJobSpec) DeepCopy() *ProwJobSpec {
	if in == nil {
		return nil
	}
	out := new(ProwJobSpec)
	in.DeepCopyInto(out)
	return out
}

// DeepCopyInto is an autogenerated deepcopy function, copying the receiver, writing into out. in must be non-nil.
func (in *ProwJobStatus) DeepCopyInto(out *ProwJobStatus) {
	*out = *in
	in.StartTime.DeepCopyInto(&out.StartTime)
	if in.CompletionTime != nil {
		in, out := &in.CompletionTime, &out.CompletionTime
		*out = (*in).DeepCopy()
	}
	if in.PrevReportStates != nil {
		in, out := &in.PrevReportStates, &out.PrevReportStates
		*out = make(map[string]ProwJobState, len(*in))
		for key, val := range *in {
			(*out)[key] = val
		}
	}
	return
}

// DeepCopy is an autogenerated deepcopy function, copying the receiver, creating a new ProwJobStatus.
func (in *ProwJobStatus) DeepCopy() *ProwJobStatus {
	if in == nil {
		return nil
	}
	out := new(ProwJobStatus)
	in.DeepCopyInto(out)
	return out
}

// DeepCopyInto is an autogenerated deepcopy function, copying the receiver, writing into out. in must be non-nil.
func (in *Pull) DeepCopyInto(out *Pull) {
	*out = *in
	return
}

// DeepCopy is an autogenerated deepcopy function, copying the receiver, creating a new Pull.
func (in *Pull) DeepCopy() *Pull {
	if in == nil {
		return nil
	}
	out := new(Pull)
	in.DeepCopyInto(out)
	return out
}

// DeepCopyInto is an autogenerated deepcopy function, copying the receiver, writing into out. in must be non-nil.
func (in *Refs) DeepCopyInto(out *Refs) {
	*out = *in
	if in.Pulls != nil {
		in, out := &in.Pulls, &out.Pulls
		*out = make([]Pull, len(*in))
		copy(*out, *in)
	}
	return
}

// DeepCopy is an autogenerated deepcopy function, copying the receiver, creating a new Refs.
func (in *Refs) DeepCopy() *Refs {
	if in == nil {
		return nil
	}
	out := new(Refs)
	in.DeepCopyInto(out)
	return out
}

// DeepCopyInto is an autogenerated deepcopy function, copying the receiver, writing into out. in must be non-nil.
func (in *UtilityImages) DeepCopyInto(out *UtilityImages) {
	*out = *in
	return
}

// DeepCopy is an autogenerated deepcopy function, copying the receiver, creating a new UtilityImages.
func (in *UtilityImages) DeepCopy() *UtilityImages {
	if in == nil {
		return nil
	}
	out := new(UtilityImages)
	in.DeepCopyInto(out)
	return out
}
