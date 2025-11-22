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
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	s4tv1alpha1 "s4t-rbac-operator/api/v1alpha1"

	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// finalizer used to avoiding CR project deletion before the cleanup of the Rbac Operator
const projectFinalizer = "s4t.s4t.io/finalizer"

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

	if !project.ObjectMeta.DeletionTimestamp.IsZero() {
		log.Info("Project resource is being deleted, starting cleanup", "name", project.Name, "namespace", project.Namespace)
		return r.handleRBACDeletion(ctx, &project)
	}

	if project.Spec.Owner == "" {
		log.Info("Project has no owner set (webhook must populate spec.owner field")
		return ctrl.Result{}, nil
	}

	log.Info("Reconciling Project resource", "name", project.Name, "namespace", project.Namespace, "generation", project.Generation)

	// Example of Updating RBAC
	if project.Status.RBACReady && (project.Status.S4TProjectID != project.Spec.ProjectName ||
		project.Annotations["last-owner"] != project.Spec.Owner) {
		log.Info("Detected spec change --> reconfiguring RBAC ")

		updateErr := r.cleanUpRBAC(ctx, project.Spec.ProjectName)
		if updateErr != nil {
			return ctrl.Result{}, updateErr
		}

		// TODO --> (implement) apply the desired state
	}

	err := r.handleCreateRBAC(ctx, &project)
	if err != nil {
		return ctrl.Result{}, err
	} else {
		log.Info("RBAC setup completed", "namespace", project.Spec.ProjectName, "owner", project.Spec.Owner)
		return ctrl.Result{}, nil
	}

}

// ----------------- Functions for manage a dinamic RBAC configuration that reflects S4T Projects -----------------------------------------------------------------

func (r *ProjectReconciler) handleCreateRBAC(ctx context.Context, project *s4tv1alpha1.Project) error {
	log := logf.FromContext(ctx)

	log.Info("Starting RBAC setup",
		"projectName", project.Spec.ProjectName,
		"owner", project.Spec.Owner)

	if !controllerutil.ContainsFinalizer(project, projectFinalizer) {
		log.Info("Adding finalizer to Project", "finalizer", projectFinalizer)
		controllerutil.AddFinalizer(project, projectFinalizer)
		err := r.Update(ctx, project)
		if err != nil {
			return err
		}
	}

	namespace := project.Spec.ProjectName

	createNamespaceErr := r.createNamespace(ctx, namespace, project)
	if createNamespaceErr != nil {
		return createNamespaceErr
	}

	createRoleError := r.createRole(ctx, namespace)
	if createRoleError != nil {
		return createRoleError
	}

	createRoleBindingErr := r.createRoleBinding(ctx, namespace, project.Spec.Owner)
	if createRoleBindingErr != nil {
		return createRoleBindingErr
	}

	log.Info("RBAC setup completed successfully", "namespace", namespace, "owner", project.Spec.Owner)

	project.Status.NamespaceReady = true
	project.Status.RBACReady = true

	apimeta.SetStatusCondition(&project.Status.Conditions, metav1.Condition{
		Type:               "Ready",
		Status:             metav1.ConditionTrue,
		ObservedGeneration: project.Generation,
		Reason:             "RBACConfigured",
		Message:            "RBAC and namespace created successfully",
	})

	if err := r.Status().Update(ctx, project); err != nil {
		return err
	}

	return nil
}

func (r *ProjectReconciler) handleRBACDeletion(ctx context.Context, project *s4tv1alpha1.Project) (ctrl.Result, error) {
	if err := r.cleanUpRBAC(ctx, project.Spec.ProjectName); err != nil {
		return ctrl.Result{}, err
	}
	controllerutil.RemoveFinalizer(project, projectFinalizer)
	updateErr := r.Update(ctx, project)
	if updateErr != nil {
		return ctrl.Result{}, updateErr
	}
	return ctrl.Result{}, nil
}

func (r *ProjectReconciler) createNamespace(ctx context.Context, namespace string, project *s4tv1alpha1.Project) error {
	log := logf.FromContext(ctx)
	ns := &corev1.Namespace{}
	err := r.Get(ctx, types.NamespacedName{Name: namespace}, ns)
	if err != nil && !errors.IsNotFound(err) {
		return err
	}
	if errors.IsNotFound(err) {
		log.Info("Creating Namespace", "name", namespace)
		ns = r.buildNamespace(namespace, project.Spec.ProjectName)
		return r.Create(ctx, ns)
	}
	log.Info("Namespace already exists", "name", namespace)
	return nil
}

func (r *ProjectReconciler) createRole(ctx context.Context, namespace string) error {
	log := logf.FromContext(ctx)
	role := &rbacv1.Role{}
	err := r.Get(ctx, types.NamespacedName{Name: "project-owner", Namespace: namespace}, role)
	if err != nil && !errors.IsNotFound(err) {
		return err
	}

	if errors.IsNotFound(err) {
		log.Info("Creating Role", "name", "project-owner", "namespace", namespace)
		role = r.buildRole(namespace)
		return r.Create(ctx, role)
	}

	log.Info("Role already exists", "name", "project-owner", "namespace", namespace)
	return nil
}

func (r *ProjectReconciler) createRoleBinding(ctx context.Context, namespace string, owner string) error {
	log := logf.FromContext(ctx)
	rb := &rbacv1.RoleBinding{}
	err := r.Get(ctx, types.NamespacedName{Name: "project-owner-binding", Namespace: namespace}, rb)
	if err != nil && !errors.IsNotFound(err) {
		return err
	}
	if errors.IsNotFound(err) {
		log.Info("Creating RoleBinding", "name", "project-owner-binding", "namespace", namespace, "owner", owner)
		rb = r.buildRoleBinding(namespace, owner)
		return r.Create(ctx, rb)
	}

	log.Info("RoleBinding already exists", "name", "project-owner-binding", "namespace", namespace)
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

func (r *ProjectReconciler) cleanUpRBAC(ctx context.Context, namespace string) error {

	log := logf.FromContext(ctx)

	log.Info("Starting RBAC cleanup", "namespace", namespace)

	rb := &rbacv1.RoleBinding{}
	if err := r.Get(ctx, types.NamespacedName{Name: "project-owner-binding", Namespace: namespace}, rb); err == nil {
		log.Info("Deleting RoleBinding", "name", rb.Name, "namespace", namespace)
		if err := r.Delete(ctx, rb); err != nil && !errors.IsNotFound(err) {
			return err
		}
	} else if !errors.IsNotFound(err) {
		return err
	}

	role := &rbacv1.Role{}
	if err := r.Get(ctx, types.NamespacedName{Name: "project-owner", Namespace: namespace}, role); err == nil {
		log.Info("Deleting Role", "name", role.Name, "namespace", namespace)
		if err := r.Delete(ctx, role); err != nil && !errors.IsNotFound(err) {
			return err
		}
	} else if !errors.IsNotFound(err) {
		return err
	}

	ns := &corev1.Namespace{}
	if err := r.Get(ctx, types.NamespacedName{Name: namespace}, ns); err == nil {
		log.Info("Deleting Namespace", "name", ns.Name)
		if err := r.Delete(ctx, ns); err != nil && !errors.IsNotFound(err) {
			return err
		}
	} else if !errors.IsNotFound(err) {
		return err
	}

	log.Info("RBAC cleanup completed", "namespace", namespace)
	return nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *ProjectReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&s4tv1alpha1.Project{}).
		Named("project").
		Complete(r)
}
