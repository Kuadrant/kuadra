/*
Copyright 2023.

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

	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	"github.com/sethvargo/go-password/password"

	kuadrav1 "github.com/Kuadrant/kuadra/api/v1"
)

func contains[T comparable](slice []T, val T) bool {
	for _, element := range slice {
		if element == val {
			return true
		}
	}
	return false
}

func getDifference[T comparable](desired []T, current []T) (wanted []T, unwanted []T) {
	for _, val := range desired {
		if !contains[T](current, val) {
			wanted = append(wanted, val)
		}
	}
	for _, val := range current {
		if !contains[T](desired, val) {
			unwanted = append(unwanted, val)
		}
	}
	return wanted, unwanted
}

// AwsAccountReconciler reconciles a AwsAccount object
type AwsAccountReconciler struct {
	client.Client
	Scheme     *runtime.Scheme
	IamWrapper IamWrapper
}

//+kubebuilder:rbac:groups=kuadra.kuadrant.io,resources=awsaccounts,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=kuadra.kuadrant.io,resources=awsaccounts/status,verbs=get;update;patch
//+kubebuilder:rbac:groups=kuadra.kuadrant.io,resources=awsaccounts/finalizers,verbs=update

// Reconcile is part of the main kubernetes reconciliation loop which aims to
// move the current state of the cluster closer to the desired state.
// TODO(user): Modify the Reconcile function to compare the state specified by
// the AwsAccount object against the actual cluster state, and then
// perform operations to make the cluster state reflect the state specified by
// the user.
//
// For more details, check Reconcile and its Result here:
// - https://pkg.go.dev/sigs.k8s.io/controller-runtime@v0.14.4/pkg/reconcile
func (r *AwsAccountReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := log.FromContext(ctx)

	// Get AwsAccount object
	var awsAccount kuadrav1.AwsAccount
	if err := r.Get(ctx, req.NamespacedName, &awsAccount); err != nil {
		log.Error(err, "unable to fetch AwsAccount")
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	if awsAccount.Status.Account == kuadrav1.CreatingUser || awsAccount.Status.Account == "" {
		user, err := r.IamWrapper.CreateUser(awsAccount.Spec.UserName)
		if err != nil {
			log.Error(err, "unable to create IAM user")
			return ctrl.Result{}, err
		}
		awsAccount.Status.Account = kuadrav1.CreatingLoginProfile
		if err = r.Status().Update(ctx, &awsAccount); err != nil {
			log.Error(err, "unable to update awsAccount status")
			return ctrl.Result{}, err
		}
		log.V(1).Info("Created user", "user:", user)
	}

	if awsAccount.Status.Account == kuadrav1.CreatingLoginProfile {
		pass, err := password.Generate(20, 3, 3, false, true)
		if err != nil {
			log.Error(err, "unable to generate password")
			return ctrl.Result{}, err
		}
		_, err = r.IamWrapper.CreateLoginProfile(pass, awsAccount.Spec.UserName, true)
		if err != nil {
			log.Error(err, "unable to create login profile")
			return ctrl.Result{}, err
		}

		// TODO: Save this to a secret in user's namespace
		log.V(1).Info("Temporary password", "password", pass)

		awsAccount.Status.Account = kuadrav1.CreatingAccessKey
		if err = r.Status().Update(ctx, &awsAccount); err != nil {
			log.Error(err, "unable to update awsAccount status")
			return ctrl.Result{}, err
		}
		log.V(1).Info("Created login profile")
	}

	if awsAccount.Status.Account == kuadrav1.CreatingAccessKey {
		accessKey, err := r.IamWrapper.CreateAccessKeyPair(awsAccount.Spec.UserName)
		if err != nil {
			log.Error(err, "unable to create access key")
			return ctrl.Result{}, err
		}

		// TODO: Save these to a secret in user's namespace
		log.V(1).Info("Credentials", "access key ID:", accessKey.AccessKeyId, "Access key secret", accessKey.SecretAccessKey)

		awsAccount.Status.Account = kuadrav1.Created
		if err = r.Status().Update(ctx, &awsAccount); err != nil {
			log.Error(err, "unable to update awsAccount status")
			return ctrl.Result{}, err
		}
		log.V(1).Info("Created access key")
	}

	// Reconcile user groups
	groupsToAddUserTo, _ := getDifference[string](awsAccount.Spec.Groups, awsAccount.Status.UserGroups)

	for _, group := range groupsToAddUserTo {
		if _, err := r.IamWrapper.AddUserToGroup(group, awsAccount.Spec.UserName); err != nil {
			log.Error(err, "unable to add user to group", "group name", group)
			return ctrl.Result{}, err
		}
		log.V(1).Info("Added user to group", "group name:", group)

		awsAccount.Status.UserGroups = append(awsAccount.Status.UserGroups, group)
		if err := r.Status().Update(ctx, &awsAccount); err != nil {
			log.Error(err, "unable to update awsAccount status after adding user to group", "group name", group)
			return ctrl.Result{}, err
		}
	}

	// for _, group := range groupsToRemoveUserFrom {
	// 	if _, err := r.IamWrapper.RemoveUserFromGroup(group, awsAccount.Spec.UserName); err != nil {
	// 		log.Error(err, "unable to remove user from group", "group name", group)
	// 		return ctrl.Result{}, err
	// 	}
	// 	log.V(1).Info("Removed user from group", "group name:", group)

	// 	// TODO: delete element from and update awsAccount.Status.UserGroups
	// 	if err := r.Status().Update(ctx, &awsAccount); err != nil {
	// 		log.Error(err, "unable to update awsAccount status after removing user from group", "group name", group)
	// 		return ctrl.Result{}, err
	// 	}
	// }

	return ctrl.Result{}, nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *AwsAccountReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&kuadrav1.AwsAccount{}).
		Complete(r)
}
