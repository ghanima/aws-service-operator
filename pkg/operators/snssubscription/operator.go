// >>>>>>> DO NOT EDIT THIS FILE <<<<<<<<<<
// This file is autogenerated via `aws-operator generate`
// If you'd like the change anything about this file make edits to the .templ
// file in the pkg/codegen/assets directory.

package snssubscription

import (
	"context"
	"github.com/awslabs/aws-service-operator/pkg/helpers"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"reflect"

	awsclient "github.com/awslabs/aws-service-operator/pkg/client/clientset/versioned/typed/service-operator.aws/v1alpha1"
	"github.com/awslabs/aws-service-operator/pkg/config"
	"github.com/awslabs/aws-service-operator/pkg/operator"
	"github.com/awslabs/aws-service-operator/pkg/queue"
	"github.com/awslabs/aws-service-operator/pkg/queuemanager"
	"github.com/iancoleman/strcase"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/client-go/tools/cache"
	"strings"

	awsV1alpha1 "github.com/awslabs/aws-service-operator/pkg/apis/service-operator.aws/v1alpha1"
)

// Operator represents a controller object for object store custom resources
type Operator struct {
	config       config.Config
	topicARN     string
	queueManager *queuemanager.QueueManager
}

// NewOperator create controller for watching object store custom resources created
func NewOperator(config config.Config, queueManager *queuemanager.QueueManager) *Operator {
	queuectrl := queue.New(config, config.AWSClientset, 10)
	topicARN, _ := queuectrl.Register("snssubscription")
	queueManager.Add(topicARN, queuemanager.HandlerFunc(QueueUpdater))

	return &Operator{
		config:       config,
		topicARN:     topicARN,
		queueManager: queueManager,
	}
}

// StartWatch watches for instances of Object Store custom resources and acts on them
func (c *Operator) StartWatch(ctx context.Context, namespace string) {
	resourceHandlers := cache.ResourceEventHandlerFuncs{
		AddFunc:    c.onAdd,
		UpdateFunc: c.onUpdate,
		DeleteFunc: c.onDelete,
	}

	oper := operator.New("snssubscriptions", namespace, resourceHandlers, c.config.AWSClientset.RESTClient())
	oper.Watch(&awsV1alpha1.SNSSubscription{}, ctx.Done())
}

// QueueUpdater will take the messages from the queue and process them
func QueueUpdater(config config.Config, msg *queuemanager.MessageBody) error {
	logger := config.Logger
	var name, namespace string
	if msg.Updatable {
		name = msg.ResourceName
		namespace = msg.Namespace
	} else {
		clientSet, _ := awsclient.NewForConfig(config.RESTConfig)
		resources, err := clientSet.SNSSubscriptions("").List(metav1.ListOptions{})
		if err != nil {
			logger.WithError(err).Error("error getting snssubscriptions")
			return err
		}
		for _, resource := range resources.Items {
			if resource.Status.StackID == msg.ParsedMessage["StackId"] {
				name = resource.Name
				namespace = resource.Namespace
			}
		}
	}

	if name != "" && namespace != "" {
		annotations := map[string]string{
			"StackID":      msg.ParsedMessage["StackId"],
			"StackName":    msg.ParsedMessage["StackName"],
			"ResourceType": msg.ParsedMessage["ResourceType"],
		}
		if msg.ParsedMessage["ResourceStatus"] == "ROLLBACK_COMPLETE" {
			obj, err := deleteStack(config, name, namespace, msg.ParsedMessage["StackId"])
			if err != nil {
				return err
			}
			config.Recorder.AnnotatedEventf(obj, annotations, corev1.EventTypeWarning, strcase.ToCamel(strings.ToLower(msg.ParsedMessage["ResourceStatus"])), msg.ParsedMessage["ResourceStatusReason"])
		} else if msg.ParsedMessage["ResourceStatus"] == "DELETE_COMPLETE" {
			obj, err := updateStatus(config, name, namespace, msg.ParsedMessage["StackId"], msg.ParsedMessage["ResourceStatus"], msg.ParsedMessage["ResourceStatusReason"])
			if err != nil {
				return err
			}
			config.Recorder.AnnotatedEventf(obj, annotations, corev1.EventTypeWarning, strcase.ToCamel(strings.ToLower(msg.ParsedMessage["ResourceStatus"])), msg.ParsedMessage["ResourceStatusReason"])
			err = incrementRollbackCount(config, name, namespace)
			if err != nil {
				return err
			}
		} else {
			obj, err := updateStatus(config, name, namespace, msg.ParsedMessage["StackId"], msg.ParsedMessage["ResourceStatus"], msg.ParsedMessage["ResourceStatusReason"])
			if err != nil {
				return err
			}
			config.Recorder.AnnotatedEventf(obj, annotations, corev1.EventTypeNormal, strcase.ToCamel(strings.ToLower(msg.ParsedMessage["ResourceStatus"])), msg.ParsedMessage["ResourceStatusReason"])
		}

	}

	return nil
}

func (c *Operator) onAdd(obj interface{}) {
	s := obj.(*awsV1alpha1.SNSSubscription).DeepCopy()
	if s.Status.ResourceStatus == "" || s.Status.ResourceStatus == "DELETE_COMPLETE" {
		cft := New(c.config, s, c.topicARN)
		output, err := cft.CreateStack()
		if err != nil {
			c.config.Logger.WithError(err).Errorf("error creating snssubscription '%s'", s.Name)
			return
		}
		c.config.Logger.Infof("added snssubscription '%s' with stackID '%s'", s.Name, string(*output.StackId))
		c.config.Logger.Infof("view at https://console.aws.amazon.com/cloudformation/home?#/stack/detail?stackId=%s", string(*output.StackId))

		_, err = updateStatus(c.config, s.Name, s.Namespace, string(*output.StackId), "CREATE_IN_PROGRESS", "")
		if err != nil {
			c.config.Logger.WithError(err).Error("error updating status")
		}
	}
}

func (c *Operator) onUpdate(oldObj, newObj interface{}) {
	oo := oldObj.(*awsV1alpha1.SNSSubscription).DeepCopy()
	no := newObj.(*awsV1alpha1.SNSSubscription).DeepCopy()

	if no.Status.ResourceStatus == "DELETE_COMPLETE" {
		c.onAdd(no)
	}
	if helpers.IsStackComplete(oo.Status.ResourceStatus, false) && !reflect.DeepEqual(oo.Spec, no.Spec) {
		cft := New(c.config, oo, c.topicARN)
		output, err := cft.UpdateStack(no)
		if err != nil {
			c.config.Logger.WithError(err).Errorf("error updating snssubscription '%s' with new params %+v and old %+v", no.Name, no, oo)
			return
		}
		c.config.Logger.Infof("updated snssubscription '%s' with params '%s'", no.Name, string(*output.StackId))
		c.config.Logger.Infof("view at https://console.aws.amazon.com/cloudformation/home?#/stack/detail?stackId=%s", string(*output.StackId))

		_, err = updateStatus(c.config, oo.Name, oo.Namespace, string(*output.StackId), "UPDATE_IN_PROGRESS", "")
		if err != nil {
			c.config.Logger.WithError(err).Error("error updating status")
		}
	}
}

func (c *Operator) onDelete(obj interface{}) {
	s := obj.(*awsV1alpha1.SNSSubscription).DeepCopy()
	cft := New(c.config, s, c.topicARN)
	err := cft.DeleteStack()
	if err != nil {
		c.config.Logger.WithError(err).Errorf("error deleting snssubscription '%s'", s.Name)
		return
	}

	c.config.Logger.Infof("deleted snssubscription '%s'", s.Name)
}
func incrementRollbackCount(config config.Config, name string, namespace string) error {
	logger := config.Logger
	clientSet, _ := awsclient.NewForConfig(config.RESTConfig)
	resource, err := clientSet.SNSSubscriptions(namespace).Get(name, metav1.GetOptions{})
	if err != nil {
		logger.WithError(err).Error("error getting snssubscriptions")
		return err
	}

	resourceCopy := resource.DeepCopy()
	resourceCopy.Spec.RollbackCount = resourceCopy.Spec.RollbackCount + 1

	_, err = clientSet.SNSSubscriptions(namespace).Update(resourceCopy)
	if err != nil {
		logger.WithError(err).Error("error updating resource")
		return err
	}
	return nil
}

func updateStatus(config config.Config, name string, namespace string, stackID string, status string, reason string) (*awsV1alpha1.SNSSubscription, error) {
	logger := config.Logger
	clientSet, _ := awsclient.NewForConfig(config.RESTConfig)
	resource, err := clientSet.SNSSubscriptions(namespace).Get(name, metav1.GetOptions{})
	if err != nil {
		logger.WithError(err).Error("error getting snssubscriptions")
		return nil, err
	}

	resourceCopy := resource.DeepCopy()
	resourceCopy.Status.ResourceStatus = status
	resourceCopy.Status.ResourceStatusReason = reason
	resourceCopy.Status.StackID = stackID

	if helpers.IsStackComplete(status, false) {
		cft := New(config, resourceCopy, "")
		outputs, err := cft.GetOutputs()
		if err != nil {
			logger.WithError(err).Error("error getting outputs")
		}
		resourceCopy.Output.SubscriptionARN = outputs["SubscriptionARN"]
	}

	_, err = clientSet.SNSSubscriptions(namespace).Update(resourceCopy)
	if err != nil {
		logger.WithError(err).Error("error updating resource")
		return nil, err
	}

	if helpers.IsStackComplete(status, false) {
		err = syncAdditionalResources(config, resourceCopy)
		if err != nil {
			logger.WithError(err).Info("error syncing resources")
		}
	}
	return resourceCopy, nil
}

func deleteStack(config config.Config, name string, namespace string, stackID string) (*awsV1alpha1.SNSSubscription, error) {
	logger := config.Logger
	clientSet, _ := awsclient.NewForConfig(config.RESTConfig)
	resource, err := clientSet.SNSSubscriptions(namespace).Get(name, metav1.GetOptions{})
	if err != nil {
		logger.WithError(err).Error("error getting snssubscriptions")
		return nil, err
	}

	cft := New(config, resource, "")
	err = cft.DeleteStack()
	if err != nil {
		return nil, err
	}

	err = cft.WaitUntilStackDeleted()
	return resource, err
}

func syncAdditionalResources(config config.Config, s *awsV1alpha1.SNSSubscription) (err error) {
	clientSet, _ := awsclient.NewForConfig(config.RESTConfig)
	resource, err := clientSet.SNSSubscriptions(s.Namespace).Get(s.Name, metav1.GetOptions{})
	if err != nil {
		return err
	}
	resource = resource.DeepCopy()

	_, err = clientSet.SNSSubscriptions(s.Namespace).Update(resource)
	if err != nil {
		return err
	}
	return nil
}
