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

package controllers

import (
	"context"
	"fmt"
	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/workqueue"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/controller-runtime/pkg/source"

	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	corev1 "k8s.io/api/core/v1"
	myappv1 "kubebuilder/api/v1"
)

// RedisReconciler reconciles a Redis object
type RedisReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

//+kubebuilder:rbac:groups=myapp.demo.kubebuilder.io,resources=redis,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=myapp.demo.kubebuilder.io,resources=redis/status,verbs=get;update;patch
//+kubebuilder:rbac:groups=myapp.demo.kubebuilder.io,resources=redis/finalizers,verbs=update

// Reconcile is part of the main kubernetes reconciliation loop which aims to
// move the current state of the cluster closer to the desired state.
// TODO(user): Modify the Reconcile function to compare the state specified by
// the Redis object against the actual cluster state, and then
// perform operations to make the cluster state reflect the state specified by
// the user.
//
// For more details, check Reconcile and its Result here:
// - https://pkg.go.dev/sigs.k8s.io/controller-runtime@v0.13.1/pkg/reconcile
func (r *RedisReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	redis := &myappv1.Redis{}
	err := r.Get(ctx, req.NamespacedName, redis)
	if err != nil {
		logger.Error(err, "get resource failed")

		return ctrl.Result{}, err
	}

	logger.Info("redis obj:", "port", redis.Spec.Port, "num", redis.Spec.Num)

	podNames := GetRedisPodName(redis)
	if !redis.DeletionTimestamp.IsZero() || len(redis.Finalizers) > redis.Spec.Num {
		return ctrl.Result{}, r.clearRedis(ctx, redis)
	}

	for _, name := range podNames {
		logger.Info("pod names:", "item", name)
	}

	var updateFlag bool
	for _, name := range podNames {
		finalizerPodName, err := CreateRedis(name, r.Client, redis, r.Scheme)
		if err != nil {
			logger.Error(err, "create pod failed", "name", name)
			return ctrl.Result{}, err
		}

		if finalizerPodName == "" {
			continue
		}

		redis.Finalizers = append(redis.Finalizers, finalizerPodName)
		updateFlag = true
	}

	if updateFlag {
		err = r.Client.Update(ctx, redis)
	}

	return ctrl.Result{}, err
}

func (r *RedisReconciler) clearRedis(ctx context.Context, redisConfig *myappv1.Redis) error {
	logger := log.FromContext(ctx)
	var deletedPodNames []string
	position := redisConfig.Spec.Num
	if (len(redisConfig.Finalizers) - redisConfig.Spec.Num) != 0 {
		deletedPodNames = redisConfig.Finalizers[position:]
		redisConfig.Finalizers = redisConfig.Finalizers[:position]
	} else {
		deletedPodNames = redisConfig.Finalizers[:]
		redisConfig.Finalizers = []string{}
	}

	for _, finalizer := range deletedPodNames {
		logger.Info("delete pod", "name", finalizer)
		err := r.Client.Delete(ctx, &corev1.Pod{
			ObjectMeta: v1.ObjectMeta{
				Name:      finalizer,
				Namespace: redisConfig.Namespace,
			},
		})

		if err != nil {
			return err
		}
	}

	return r.Client.Update(ctx, redisConfig)
}

// SetupWithManager sets up the controller with the Manager.
func (r *RedisReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&myappv1.Redis{}).
		Watches(&source.Kind{Type: &corev1.Pod{}}, handler.Funcs{DeleteFunc: r.podDeleteHandler}).
		Complete(r)
}

func (r *RedisReconciler) podDeleteHandler(event event.DeleteEvent, limitingInterface workqueue.RateLimitingInterface) {
	logger := log.FromContext(context.Background())
	logger.Info("deleted pod", "name", event.Object.GetName())
	for _, reference := range event.Object.GetOwnerReferences() {
		if reference.Kind == "Redis" && reference.Name == "redis-sample" {
			limitingInterface.Add(reconcile.Request{
				NamespacedName: types.NamespacedName{
					Namespace: event.Object.GetNamespace(),
					Name:      reference.Name,
				},
			})
		}
	}
}

func CreateRedis(podName string, client client.Client, redisConfig *myappv1.Redis, schema *runtime.Scheme) (string, error) {
	if isExist(podName, redisConfig, client) {
		return podName, nil
	}
	newPod := &corev1.Pod{}
	newPod.Name = podName
	newPod.Namespace = redisConfig.Namespace
	newPod.Spec.Containers = []corev1.Container{
		{
			Name:            redisConfig.Name,
			Image:           "redis:latest",
			ImagePullPolicy: corev1.PullIfNotPresent,
			Ports: []corev1.ContainerPort{
				{
					ContainerPort: int32(redisConfig.Spec.Port),
				},
			},
		},
	}

	if err := controllerutil.SetControllerReference(redisConfig, newPod, schema); err != nil {
		return podName, err
	}
	return podName, client.Create(context.Background(), newPod)
}

func isExist(podName string, redis *myappv1.Redis, client client.Client) bool {
	err := client.Get(context.Background(), types.NamespacedName{
		Name:      podName,
		Namespace: redis.Namespace,
	}, &corev1.Pod{})

	if err != nil {
		return false
	}
	return true
}

func GetRedisPodName(redis *myappv1.Redis) []string {
	podNames := make([]string, redis.Spec.Num)
	for i := 0; i < redis.Spec.Num; i++ {
		podNames[i] = fmt.Sprintf("%s-%d", redis.Name, i)
	}
	return podNames
}
