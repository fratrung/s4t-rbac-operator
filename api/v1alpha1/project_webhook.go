package v1alpha1

import (
	"context"
	"encoding/json"

	admission "sigs.k8s.io/controller-runtime/pkg/webhook/admission"
)

type ProjectMutator struct{}

var _ admission.Handler = &ProjectMutator{}

func (m *ProjectMutator) Handle(ctx context.Context, req admission.Request) admission.Response {
	project := &Project{}

	if err := json.Unmarshal(req.Object.Raw, project); err != nil {
		return admission.Errored(400, err)
	}

	if project.Spec.Owner == "" {
		project.Spec.Owner = req.UserInfo.Username
	}

	if project.Spec.Namespace == "" {
		project.Spec.Namespace = project.Spec.ProjectName
	}

	mutated, err := json.Marshal(project)
	if err != nil {
		return admission.Errored(500, err)
	}

	return admission.PatchResponseFromRaw(req.Object.Raw, mutated)
}
