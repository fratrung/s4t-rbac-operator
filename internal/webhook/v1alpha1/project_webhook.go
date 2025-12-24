/*
Copyright 2025.

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

package v1alpha1

import (
	"context"
	"fmt"
	"strings"

	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/webhook"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	s4tv1alpha1 "s4t-rbac-operator/api/v1alpha1"
)

// nolint:unused
// log is for logging in this package.
var projectlog = logf.Log.WithName("project-resource")

// SetupProjectWebhookWithManager registers the webhook for Project in the manager.
func SetupProjectWebhookWithManager(mgr ctrl.Manager) error {
	return ctrl.NewWebhookManagedBy(mgr).For(&s4tv1alpha1.Project{}).
		WithDefaulter(&ProjectCustomDefaulter{}).
		Complete()
}

// TODO(user): EDIT THIS FILE!  THIS IS SCAFFOLDING FOR YOU TO OWN!

// +kubebuilder:webhook:path=/mutate-s4t-s4t-io-v1alpha1-project,mutating=true,failurePolicy=fail,sideEffects=None,groups=s4t.s4t.io,resources=projects,verbs=create,versions=v1alpha1,name=mproject-v1alpha1.kb.io,admissionReviewVersions=v1

// ProjectCustomDefaulter struct is responsible for setting default values on the custom resource of the
// Kind Project when those are created or updated.
//
// NOTE: The +kubebuilder:object:generate=false marker prevents controller-gen from generating DeepCopy methods,
// as it is used only for temporary operations and does not need to be deeply copied.
type ProjectCustomDefaulter struct {
	// TODO(user): Add more fields as needed for defaulting
}

var _ webhook.CustomDefaulter = &ProjectCustomDefaulter{}

// Default implements webhook.CustomDefaulter so a webhook will be registered for the Kind Project.
func (d *ProjectCustomDefaulter) Default(ctx context.Context, obj runtime.Object) error {
	project, ok := obj.(*s4tv1alpha1.Project)

	if !ok {
		return fmt.Errorf("expected an Project object but got %T", obj)
	}

	req, err := admission.RequestFromContext(ctx)
	if err != nil {
		return err
	}

	user := strings.TrimSpace(req.UserInfo.Username)
	if user == "" {
		return fmt.Errorf("cannot determine caller username")
	}

	project.Spec.Owner = user

	projectlog.Info("Defaulting for Project", "name", project.GetName())

	return nil
}
