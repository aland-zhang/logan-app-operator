package phpboot

import (
	"context"
	"github.com/go-logr/logr"
	appv1 "github.com/logancloud/logan-app-operator/pkg/apis/app/v1"
	"github.com/logancloud/logan-app-operator/pkg/logan/config"
	"github.com/logancloud/logan-app-operator/pkg/logan/operator"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/record"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	logf "sigs.k8s.io/controller-runtime/pkg/runtime/log"
	"sigs.k8s.io/controller-runtime/pkg/source"
)

var log = logf.Log.WithName("logan_controller_phpboot")

// Add creates a new Boot Controller and adds it to the Manager.
// The Manager will set fields on the Controller and Start it when the Manager is Started.
func Add(mgr manager.Manager) error {
	return add(mgr, newReconciler(mgr))
}

// newReconciler returns a new reconcile.Reconciler
func newReconciler(mgr manager.Manager) reconcile.Reconciler {
	return &ReconcilePhpBoot{
		client:   mgr.GetClient(),
		scheme:   mgr.GetScheme(),
		recorder: mgr.GetRecorder("phpboot-controller"),
	}
}

// add adds a new Controller to mgr with r as the reconcile.Reconciler
func add(mgr manager.Manager, r reconcile.Reconciler) error {
	// Create a new controller
	c, err := controller.New("phpboot-controller", mgr, controller.Options{Reconciler: r})
	if err != nil {
		return err
	}

	// Watch for changes to primary resource PhpBoot
	err = c.Watch(&source.Kind{Type: &appv1.PhpBoot{}}, &handler.EnqueueRequestForObject{})
	if err != nil {
		return err
	}

	// Modify this to be the types you create(Deployment and Service) that are owned by the primary resource
	err = c.Watch(&source.Kind{Type: &appsv1.Deployment{}}, &handler.EnqueueRequestForOwner{
		IsController: true,
		OwnerType:    &appv1.PhpBoot{},
	})
	if err != nil {
		return err
	}

	err = c.Watch(&source.Kind{Type: &corev1.Service{}}, &handler.EnqueueRequestForOwner{
		IsController: true,
		OwnerType:    &appv1.PhpBoot{},
	})
	if err != nil {
		return err
	}

	return nil
}

var _ reconcile.Reconciler = &ReconcilePhpBoot{}

// ReconcilePhpBoot reconciles a PhpBoot object
type ReconcilePhpBoot struct {
	// This client, initialized using mgr.Client() above, is a split client
	// that reads objects from the cache and writes to the apiserver
	client   client.Client
	scheme   *runtime.Scheme
	recorder record.EventRecorder
}

// Reconcile reads that state of the cluster for a Boot object and makes changes based on the state read
// and what is in the Boot.Spec
// Note:
// The Controller will requeue the Request to be processed again if the returned error is non-nil or
// Result.Requeue is true, otherwise upon completion it will remove the work from the queue.
func (r *ReconcilePhpBoot) Reconcile(request reconcile.Request) (reconcile.Result, error) {
	logger := log.WithValues("phpboot", request)

	if operator.Ignore(request.Namespace) {
		return reconcile.Result{}, nil
	}

	logger.Info("Reconciling PhpBoot")

	var handler *operator.BootHandler

	// Fetch the Boot instance
	phpBoot := &appv1.PhpBoot{}
	err := r.client.Get(context.TODO(), request.NamespacedName, phpBoot)
	if err != nil {
		if errors.IsNotFound(err) {
			// Request object not found, could have been deleted after reconcile request.
			// Owned objects are automatically garbage collected. For additional cleanup logic use finalizers.
			logger.Info("Boot resource not found. Ignoring since object must be deleted")
			return reconcile.Result{}, nil
		}
		// Error reading the object - requeue the request.
		logger.Error(err, "Failed to get Boot")
		return reconcile.Result{}, err
	} else {
		handler = InitHandler(phpBoot, r.scheme, r.client, logger, r.recorder)

		changed := handler.DefaultValue()

		//Update the Boot's default Value
		if changed {
			reason := "Updating Boot with Defaulters"
			logger.Info(reason)
			err = r.client.Update(context.TODO(), phpBoot)
			if err != nil {
				logger.Info("Failed to update Boot", "boot", phpBoot)
				handler.EventFail(reason, phpBoot.Name, err)
				return reconcile.Result{Requeue: true}, nil
			}
			handler.EventNormal(reason, phpBoot.Name)
			return reconcile.Result{Requeue: true}, nil
		}
	}

	// 1. Check the existence of components, if not exist, create new one.
	result, err, requeue := handler.ReconcileCreate()
	if requeue {
		return result, err
	}

	// 2. Handle the update logic of components
	result, err, requeue = handler.ReconcileUpdate()
	if requeue {
		return result, err
	}

	result, err, requeue, updated := handler.ReconcileUpdateBootMeta()

	if updated {
		reason := "Updating Boot Meta"
		logger.Info(reason, "new", phpBoot.Annotations)
		err := r.client.Update(context.TODO(), phpBoot)
		if err != nil {
			// Other place will modify the status? So it will sometimes occur.
			logger.Info("Failed to update Boot Metadata", "err", err.Error())

			handler.EventFail(reason, phpBoot.Name, err)
			return reconcile.Result{Requeue: true}, nil
		}
		handler.EventNormal(reason, phpBoot.Name)
	}
	if requeue {
		return result, err
	}

	return reconcile.Result{}, nil
}

func InitHandler(phpBoot *appv1.PhpBoot, scheme *runtime.Scheme,
	client client.Client, logger logr.Logger, recorder record.EventRecorder) (handler *operator.BootHandler) {
	boot := phpBoot.DeepCopyBoot()
	return &operator.BootHandler{
		OperatorBoot: phpBoot,
		OperatorSpec: &phpBoot.Spec,
		OperatorMeta: &phpBoot.ObjectMeta,

		Boot:     boot,
		Config:   config.PhpConfig,
		Scheme:   scheme,
		Client:   client,
		Logger:   logger,
		Recorder: recorder,
	}
}