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
	"fmt"
	"reflect"
	"strings"
	"time"

	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/validation"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	s4tv1alpha1 "s4t-rbac-operator/api/v1alpha1"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// finalizer used to avoiding CR project deletion before the cleanup of the Rbac Operator
const projectFinalizer = "s4t.s4t.io/finalizer"

func validateTargetNamespace(ns string) error {
	// strict DNS label validation
	if errs := validation.IsDNS1123Label(ns); len(errs) > 0 {
		return fmt.Errorf("invalid projectName/namespace %q: %s", ns, strings.Join(errs, "; "))
	}

	// block well-known system namespaces
	blocked := map[string]bool{
		"default":         true,
		"kube-system":     true,
		"kube-public":     true,
		"kube-node-lease": true,
	}
	if blocked[ns] {
		return fmt.Errorf("projectName/namespace %q is reserved", ns)
	}
	return nil
}

// ProjectReconciler reconciles a Project object
type ProjectReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

// +kubebuilder:rbac:groups=s4t.s4t.io,resources=projects,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=s4t.s4t.io,resources=projects/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=s4t.s4t.io,resources=projects/finalizers,verbs=update
// RBAC resources we manage
// +kubebuilder:rbac:groups="",resources=namespaces,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=rbac.authorization.k8s.io,resources=roles;rolebindings,verbs=get;list;watch;create;update;patch;delete

func (r *ProjectReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	var project s4tv1alpha1.Project
	if err := r.Get(ctx, types.NamespacedName{Name: req.Name}, &project); err != nil {
		if errors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

	if project.Status.NamespaceReady && project.Status.RBACReady && project.ObjectMeta.DeletionTimestamp.IsZero() {
		log.V(1).Info("Project already reconciled, skipping", "project", project.Name)
		return ctrl.Result{}, nil
	}

	if !project.ObjectMeta.DeletionTimestamp.IsZero() {
		log.Info("Project resource is being deleted, starting cleanup", "project", project.Name)
		return r.handleRBACDeletion(ctx, &project)
	}

	owner := strings.TrimSpace(project.Spec.Owner)

	if owner == "" {
		log.V(1).Info("Waiting for owner to be populated by webhook")
		return ctrl.Result{}, nil
	}

	username := extractUsernameFromOIDC(owner)

	projectName := strings.TrimSpace(project.Spec.ProjectName)
	if projectName == "" {
		return r.failStatus(ctx, &project, "InvalidSpec", "spec.projectName is required")
	}
	namespace := deriveNamespace(username, projectName)

	if err := validateTargetNamespace(namespace); err != nil {
		return r.failStatus(ctx, &project, "InvalidProjectName", err.Error())
	}

	if !controllerutil.ContainsFinalizer(&project, projectFinalizer) {
		controllerutil.AddFinalizer(&project, projectFinalizer)
		if err := r.Update(ctx, &project); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{Requeue: true}, nil
	}

	log.Info("Reconciling Project resource", "name", project.Name, "generation", project.Generation)

	if err := r.ensureNamespace(ctx, namespace); err != nil {
		return ctrl.Result{}, err
	}
	if err := r.ensureRoles(ctx, &project, namespace); err != nil {
		return ctrl.Result{}, err
	}
	if err := r.ensureProjectRoleBinding(ctx, &project, namespace, username, projectName); err != nil {
		return ctrl.Result{}, err
	}

	changed := !project.Status.NamespaceReady || !project.Status.RBACReady
	if changed {
		project.Status.NamespaceReady = true
		project.Status.RBACReady = true
		apimeta.SetStatusCondition(&project.Status.Conditions, metav1.Condition{
			Type:               "Ready",
			Status:             metav1.ConditionTrue,
			ObservedGeneration: project.Generation,
			Reason:             "RBACConfigured",
			Message:            "Namespace, Role and RoleBinding configured successfully",
		})

		if err := r.Status().Update(ctx, &project); err != nil {
			if errors.IsConflict(err) {
				log.Info("Conflict updating Project status, requeueing")
				return ctrl.Result{Requeue: true}, nil
			}
			return ctrl.Result{}, err
		}
	}
	/*
		readyCond := apimeta.FindStatusCondition(project.Status.Conditions, "Ready")
		if readyCond != nil &&
			readyCond.Status == metav1.ConditionTrue &&
			readyCond.ObservedGeneration == project.Generation &&
			readyCond.Reason == "RBACConfigured" {
			createRequest, err := r.createS4TProjectRequest(&project)
			if err != nil {
				log.Info("Request Creation for External Provider failed")
			}
			ok := r.sendToExternalProvider(createRequest)
			if !ok {
				log.Info("Failed to notify external provider")
				return ctrl.Result{}, nil

			}
			extCond := apimeta.FindStatusCondition(project.Status.Conditions, "Ready")
			if extCond == nil || extCond.Reason != "ExternalSynced" {
				apimeta.SetStatusCondition(&project.Status.Conditions, metav1.Condition{
					Type:               "Ready",
					Status:             metav1.ConditionTrue,
					ObservedGeneration: project.Generation,
					Reason:             "ExternalSynced",
					Message:            "Project synced with external provider",
				})
				if err := r.Status().Update(ctx, &project); err != nil {
					return ctrl.Result{}, nil
				}
			}

		}
	*/
	return ctrl.Result{}, nil
}

func (r *ProjectReconciler) ensureNamespace(ctx context.Context, namespace string) error {
	log := logf.FromContext(ctx)

	ns := &corev1.Namespace{}
	err := r.Get(ctx, types.NamespacedName{Name: namespace}, ns)

	if err == nil {
		log.V(1).Info("Namespace already exists", "name", namespace)

		return nil
	}
	if !errors.IsNotFound(err) {
		return err
	}

	return r.Create(ctx, &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name: namespace,
		},
	})
}

func (r *ProjectReconciler) ensureRoleObj(ctx context.Context, project *s4tv1alpha1.Project, desired *rbacv1.Role) error {
	log := logf.FromContext(ctx)

	if err := controllerutil.SetControllerReference(project, desired, r.Scheme); err != nil {
		return err
	}

	existing := &rbacv1.Role{}
	err := r.Get(ctx, types.NamespacedName{Name: desired.Name, Namespace: desired.Namespace}, existing)
	if errors.IsNotFound(err) {
		log.Info("Creating Role", "name", desired.Name)
		return r.Create(ctx, desired)
	}
	if err != nil {
		return err
	}

	if !reflect.DeepEqual(existing.Rules, desired.Rules) {
		patch := client.MergeFrom(existing.DeepCopy())
		existing.Rules = desired.Rules
		log.Info("Updating Role rules", "name", desired.Name)
		return r.Patch(ctx, existing, patch)
	}

	return nil
}

func (r *ProjectReconciler) ensureRoles(ctx context.Context, project *s4tv1alpha1.Project, namespace string) error {
	if err := r.ensureRoleObj(ctx, project, r.buildAdminProjectRole(namespace)); err != nil {
		return err
	}
	if err := r.ensureRoleObj(ctx, project, r.buildManagerProjectRole(namespace)); err != nil {
		return err
	}
	if err := r.ensureRoleObj(ctx, project, r.buildUserProjectRole(namespace)); err != nil {
		return err
	}
	return nil
}

func (r *ProjectReconciler) buildAdminProjectRole(namespace string) *rbacv1.Role {
	return &rbacv1.Role{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "s4t-admin-project",
			Namespace: namespace,
		},
		Rules: []rbacv1.PolicyRule{
			{
				APIGroups: []string{"*"},
				Resources: []string{"*"},
				Verbs:     []string{"*"},
			},
		},
	}
}

func (r *ProjectReconciler) buildManagerProjectRole(namespace string) *rbacv1.Role {
	return &rbacv1.Role{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "s4t-manager-project",
			Namespace: namespace,
		},
		Rules: []rbacv1.PolicyRule{
			{
				APIGroups: []string{"*"},
				Resources: []string{"*"},
				Verbs:     []string{"get", "list", "watch"},
			},
			{
				APIGroups: []string{"", "apps", "batch", "networking.k8s.io"},
				Resources: []string{
					"pods", "services", "configmaps", "secrets", "persistentvolumeclaims",
					"deployments", "replicasets", "statefulsets", "daemonsets",
					"jobs", "cronjobs",
					"ingresses",
				},
				Verbs: []string{"create", "update", "patch", "delete"},
			},

			// optional
			{
				APIGroups: []string{""},
				Resources: []string{"pods/exec", "pods/log"},
				Verbs:     []string{"get", "create"},
			},
		},
	}
}

func (r *ProjectReconciler) buildUserProjectRole(namespace string) *rbacv1.Role {
	return &rbacv1.Role{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "s4t-user-project",
			Namespace: namespace,
		},
		Rules: []rbacv1.PolicyRule{
			{
				APIGroups: []string{"*"},
				Resources: []string{"*"},
				Verbs:     []string{"get", "list", "watch"},
			},
		},
	}
}

func (r *ProjectReconciler) ensureRoleBindingObj(ctx context.Context, project *s4tv1alpha1.Project, desired *rbacv1.RoleBinding) error {
	log := logf.FromContext(ctx)

	if err := controllerutil.SetControllerReference(project, desired, r.Scheme); err != nil {
		return err
	}

	existing := &rbacv1.RoleBinding{}
	err := r.Get(ctx, types.NamespacedName{Name: desired.Name, Namespace: desired.Namespace}, existing)
	if errors.IsNotFound(err) {
		log.Info("Creating RoleBinding", "name", desired.Name)
		return r.Create(ctx, desired)
	}
	if err != nil {
		return err
	}

	if existing.RoleRef != desired.RoleRef {
		log.Info("RoleRef changed, recreating RoleBinding", "name", desired.Name)
		if err := r.Delete(ctx, existing); err != nil && !errors.IsNotFound(err) {
			return err
		}
		return r.Create(ctx, desired)
	}

	if !reflect.DeepEqual(existing.Subjects, desired.Subjects) {
		patch := client.MergeFrom(existing.DeepCopy())
		existing.Subjects = desired.Subjects
		log.Info("Updating RoleBinding subjects", "name", desired.Name)
		return r.Patch(ctx, existing, patch)
	}

	return nil
}

func (r *ProjectReconciler) buildRoleBindingForGroup(namespace, name, roleName, group string) *rbacv1.RoleBinding {
	return &rbacv1.RoleBinding{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
		},
		Subjects: []rbacv1.Subject{
			{
				Kind:     rbacv1.GroupKind,
				Name:     group,
				APIGroup: rbacv1.GroupName,
			},
		},
		RoleRef: rbacv1.RoleRef{
			APIGroup: rbacv1.GroupName,
			Kind:     "Role",
			Name:     roleName,
		},
	}
}

func (r *ProjectReconciler) ensureProjectRoleBinding(ctx context.Context, project *s4tv1alpha1.Project, namespace, owner, projectName string) error {
	base := deriveGroupBase(owner, projectName)

	adminGroup := base + ":admin_iot_project"
	memberGroup := base + ":manager_iot_project"
	userGroup := base + ":user_iot"

	if err := r.ensureRoleBindingObj(ctx, project,
		r.buildRoleBindingForGroup(namespace, "s4t-admin-binding", "s4t-admin-project", adminGroup),
	); err != nil {
		return err
	}

	if err := r.ensureRoleBindingObj(ctx, project,
		r.buildRoleBindingForGroup(namespace, "s4t-manager-binding", "s4t-manager-project", memberGroup),
	); err != nil {
		return err
	}

	if err := r.ensureRoleBindingObj(ctx, project,
		r.buildRoleBindingForGroup(namespace, "s4t-user-binding", "s4t-user-project", userGroup),
	); err != nil {
		return err
	}
	return nil
}

func (r *ProjectReconciler) isNamespaceEmpty(ctx context.Context, namespace string) (bool, error) {
	checks := []client.ObjectList{
		&rbacv1.RoleBindingList{},
		&rbacv1.RoleList{},
		&corev1.PodList{},
		&appsv1.DeploymentList{},
		&corev1.ServiceList{},
		&corev1.PersistentVolumeClaimList{},
		&corev1.ConfigMapList{},
	}

	for _, list := range checks {
		if err := r.List(ctx, list, client.InNamespace(namespace)); err != nil {
			return false, err
		}
		if apimeta.LenList(list) > 0 {
			return false, nil
		}
	}
	return true, nil
}

func (r *ProjectReconciler) handleRBACDeletion(ctx context.Context, project *s4tv1alpha1.Project) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	projectName := strings.TrimSpace(project.Spec.ProjectName)
	owner := strings.TrimSpace(project.Spec.Owner)
	username := extractUsernameFromOIDC(owner)
	namespace := deriveNamespace(username, projectName)

	if namespace != "" {
		if err := r.cleanUpRBAC(ctx, namespace); err != nil {
			return ctrl.Result{}, err
		}

		empty, err := r.isNamespaceEmpty(ctx, namespace)
		if err != nil {
			log.Error(err, "Failed to check if namespace if empty")
			return ctrl.Result{RequeueAfter: 5 * time.Second}, nil
		}
		if !empty {
			log.Info("Namespace not yet fully cleaned, requeueing after 5 seconds", "namespace", namespace)
			return ctrl.Result{RequeueAfter: 5 * time.Second}, nil
		}
		if err := r.cleanNamespace(ctx, namespace); err != nil {
			return ctrl.Result{}, err
		}
	}
	controllerutil.RemoveFinalizer(project, projectFinalizer)
	if err := r.Update(ctx, project); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{}, nil
}

func (r *ProjectReconciler) cleanUpRBAC(ctx context.Context, namespace string) error {
	log := logf.FromContext(ctx)

	log.Info("Starting RBAC cleanup", "namespace", namespace)

	cleanRoleBindingError := r.cleanRoleBinding(ctx, namespace)
	if cleanRoleBindingError != nil {
		return cleanRoleBindingError
	}

	cleanRoleError := r.cleanRole(ctx, namespace)
	if cleanRoleError != nil {
		return cleanRoleError
	}
	/*
		cleanNamespaceError := r.cleanNamespace(ctx, namespace)
		if cleanNamespaceError != nil {
			return cleanNamespaceError
		}

		log.Info("RBAC cleanup completed", "namespace", namespace)
	*/
	return nil

}

func (r *ProjectReconciler) cleanRoleBinding(ctx context.Context, namespace string) error {
	log := logf.FromContext(ctx)
	for _, name := range []string{
		"s4t-admin-binding",
		"s4t-manager-binding",
		"s4t-user-binding",
	} {
		rb := &rbacv1.RoleBinding{}
		if err := r.Get(ctx, types.NamespacedName{Name: name, Namespace: namespace}, rb); err == nil {
			log.Info("Deleting RoleBinding", "name", name, "namespace", namespace)
			if err := r.Delete(ctx, rb); err != nil && !errors.IsNotFound(err) {
				return err
			}
		} else if !errors.IsNotFound(err) {
			return err
		}
	}
	return nil
}
func (r *ProjectReconciler) cleanRole(ctx context.Context, namespace string) error {
	log := logf.FromContext(ctx)

	for _, name := range []string{
		"s4t-admin-project",
		"s4t-manager-project",
		"s4t-user-project",
	} {
		role := &rbacv1.Role{}
		if err := r.Get(ctx, types.NamespacedName{Name: name, Namespace: namespace}, role); err == nil {
			log.Info("Deleting Role", "name", name, "namespace", namespace)
			if err := r.Delete(ctx, role); err != nil && !errors.IsNotFound(err) {
				return err
			}
		} else if !errors.IsNotFound(err) {
			return err
		}
	}
	return nil
}

func (r *ProjectReconciler) cleanNamespace(ctx context.Context, namespace string) error {
	log := logf.FromContext(ctx)
	ns := &corev1.Namespace{}
	if err := r.Get(ctx, types.NamespacedName{Name: namespace}, ns); err == nil {
		log.Info("Deleting Namespace", "name", ns.Name)
		if err := r.Delete(ctx, ns); err != nil && !errors.IsNotFound(err) {
			return err
		}
	} else if !errors.IsNotFound(err) {
		return err
	}
	return nil
}

func (r *ProjectReconciler) failStatus(ctx context.Context, project *s4tv1alpha1.Project, reason, msg string) (ctrl.Result, error) {
	log := logf.FromContext(ctx)
	apimeta.SetStatusCondition(&project.Status.Conditions, metav1.Condition{
		Type:               "Ready",
		Status:             metav1.ConditionFalse,
		ObservedGeneration: project.Generation,
		Reason:             reason,
		Message:            msg,
	})
	err := r.Status().Update(ctx, project)
	if err != nil {
		log.Error(err, "failed to update Project status")
		return ctrl.Result{RequeueAfter: 2 * time.Second}, err
	}
	return ctrl.Result{}, nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *ProjectReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&s4tv1alpha1.Project{}).
		Owns(&rbacv1.Role{}).
		Owns(&rbacv1.RoleBinding{}).
		Named("project").
		Complete(r)
}

/*
type ExternalS4TRequest struct {
	Endpoint    string
	ProjectName string
}
*/

/*
	func (r *ProjectReconciler) sendToExternalProvider(request *ExternalS4TRequest) bool {
		payload := fmt.Sprintf(`{"projectName": "%s"}`, request.ProjectName)
		body := strings.NewReader(payload)
		response, err := http.Post(request.Endpoint, "application/json", body)
		if err != nil {
			return false
		}
		defer response.Body.Close()
		if response.StatusCode != http.StatusOK {
			return false
		}
		return true
	}

	func (r *ProjectReconciler) createS4TProjectRequest(project *s4tv1alpha1.Project) (*ExternalS4TRequest, error) {
		projectName := project.Spec.ProjectName
		if projectName == "" {
			return nil, fmt.Errorf("projectName is empty")
		}

		var endpoint = "http://127.0.0.1:8787/create-project"
		return &ExternalS4TRequest{
			Endpoint:    endpoint,
			ProjectName: projectName,
		}, nil

}
func (r *ProjectReconciler) updateS4TProjectRequest() {}
func (r *ProjectReconciler) deleteS4TProjectRequest() {}
*/
