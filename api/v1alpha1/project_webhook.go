package v1alpha1

import (
	"context"
	"encoding/json"
	"strings"

	admissionv1 "k8s.io/api/admission/v1"
	admission "sigs.k8s.io/controller-runtime/pkg/webhook/admission"
)

type ProjectMutator struct{}

var _ admission.Handler = &ProjectMutator{}

func (m *ProjectMutator) Handle(ctx context.Context, req admission.Request) admission.Response {
	// mutate only on create operation
	if req.Operation != admissionv1.Create {
		return admission.Allowed("no mutation")
	}

	project := &Project{}
	if err := json.Unmarshal(req.Object.Raw, project); err != nil {
		return admission.Errored(400, err)
	}

	user := strings.TrimSpace(req.UserInfo.Username)
	if user == "" {
		return admission.Denied("cannot determine caller username")
	}

	project.Spec.Owner = user

	mutated, err := json.Marshal(project)
	if err != nil {
		return admission.Errored(500, err)
	}

	return admission.PatchResponseFromRaw(req.Object.Raw, mutated)
}
