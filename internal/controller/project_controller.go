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

package controller

import (
	"context"

	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	s4tv1alpha1 "s4t-rbac-operator/api/v1alpha1"

	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// ProjectReconciler reconciles a Project object
type ProjectReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

// +kubebuilder:rbac:groups=s4t.s4t.io,resources=projects,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=s4t.s4t.io,resources=projects/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=s4t.s4t.io,resources=projects/finalizers,verbs=update

// Reconcile is part of the main kubernetes reconciliation loop which aims to
// move the current state of the cluster closer to the desired state.
// TODO(user): Modify the Reconcile function to compare the state specified by
// the Project object against the actual cluster state, and then
// perform operations to make the cluster state reflect the state specified by
// the user.
//
// For more details, check Reconcile and its Result here:
// - https://pkg.go.dev/sigs.k8s.io/controller-runtime@v0.22.1/pkg/reconcile
func (r *ProjectReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	var project s4tv1alpha1.Project
	if err := r.Get(ctx, req.NamespacedName, &project); err != nil {
		if errors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

	namespace := project.Spec.ProjectName
	owner := project.Spec.Owner

	if owner == "" {
		log.Info("Project has no owner set (webhook must populate spec.owner field")
		return ctrl.Result{}, nil
	}

	err := r.HandleCreateRBAC(ctx, namespace, project)
	if err != nil {
		return ctrl.Result{}, err
	}

	log.Info("RBAC setup completed", "namespace", namespace, "owner", owner)
	return ctrl.Result{}, nil
}

// ----------------- Helper function for building a dinamic RBAC configuration for S4T -----------------------------------------------------------------

// encapsulate all RBAC automatic generation
func (r *ProjectReconciler) HandleCreateRBAC(ctx context.Context, namespace string, project s4tv1alpha1.Project) error {
	createNamespaceErr := r.CreateNamespace(ctx, namespace, project)
	if createNamespaceErr != nil {
		return createNamespaceErr
	}

	createRoleError := r.CreateRole(ctx, namespace)
	if createRoleError != nil {
		return createRoleError
	}

	createRoleBindingErr := r.CreateRoleBinding(ctx, namespace, project.Spec.Owner)
	if createRoleBindingErr != nil {
		return createRoleBindingErr
	}
	return nil
}

func (r *ProjectReconciler) CreateNamespace(ctx context.Context, namespace string, project s4tv1alpha1.Project) error {
	ns := &corev1.Namespace{}
	err := r.Get(ctx, types.NamespacedName{Name: namespace}, ns)
	if errors.IsNotFound(err) {
		ns = r.buildNamespace(namespace, project.Spec.ProjectName)
		if createError := r.Create(ctx, ns); createError != nil {
			return createError
		}
	} else {
		return err
	}
	return nil
}

func (r *ProjectReconciler) CreateRole(ctx context.Context, namespace string) error {
	role := &rbacv1.Role{}
	err := r.Get(ctx, types.NamespacedName{Name: "project-owner", Namespace: namespace}, role)
	if errors.IsNotFound(err) {
		role = r.buildRole(namespace)
		if createRoleError := r.Create(ctx, role); createRoleError != nil {
			return createRoleError
		}
	} else {
		return err
	}
	return nil
}

func (r *ProjectReconciler) CreateRoleBinding(ctx context.Context, namespace string, owner string) error {
	rb := &rbacv1.RoleBinding{}
	err := r.Get(ctx, types.NamespacedName{Name: "project-owner-binding", Namespace: namespace}, rb)
	if errors.IsNotFound(err) {
		rb = r.buildRoleBinding(namespace, owner)
		if createRoleBindingErr := r.Create(ctx, rb); createRoleBindingErr != nil {
			return createRoleBindingErr
		}
	} else {
		return err
	}
	return nil
}

func (r *ProjectReconciler) buildNamespace(namespace string, projectName string) *corev1.Namespace {
	return &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name: namespace,
			Labels: map[string]string{
				"s4t/project": projectName,
			},
		},
	}
}

func (r *ProjectReconciler) buildRole(namespace string) *rbacv1.Role {
	return &rbacv1.Role{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "project-owner",
			Namespace: namespace,
		},
		Rules: []rbacv1.PolicyRule{
			{
				APIGroups: []string{"", "apps", "batch", "s4t.s4t.io"},
				Resources: []string{"*"},
				Verbs:     []string{"*"},
			},
		},
	}
}

func (r *ProjectReconciler) buildRoleBinding(namespace string, owner string) *rbacv1.RoleBinding {
	return &rbacv1.RoleBinding{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "project-owner-binding",
			Namespace: namespace,
		},
		Subjects: []rbacv1.Subject{
			{
				Kind:     rbacv1.UserKind,
				Name:     owner,
				APIGroup: rbacv1.GroupName,
			},
		},
		RoleRef: rbacv1.RoleRef{
			APIGroup: rbacv1.GroupName,
			Kind:     "Role",
			Name:     "project-owner",
		},
	}
}

// SetupWithManager sets up the controller with the Manager.
func (r *ProjectReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&s4tv1alpha1.Project{}).
		Named("project").
		Complete(r)
}
