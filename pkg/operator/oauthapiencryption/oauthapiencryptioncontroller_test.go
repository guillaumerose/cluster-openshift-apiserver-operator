package oauthapiencryption

import (
	"context"
	"fmt"
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	"k8s.io/apimachinery/pkg/api/meta"
	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/diff"
	"k8s.io/client-go/kubernetes/fake"
	corev1listers "k8s.io/client-go/listers/core/v1"
	clientgotesting "k8s.io/client-go/testing"
	"k8s.io/client-go/tools/cache"

	"github.com/openshift/cluster-openshift-apiserver-operator/pkg/operator/operatorclient"
	"github.com/openshift/library-go/pkg/controller/factory"
	"github.com/openshift/library-go/pkg/operator/encryption/encryptionconfig"
	encryptionsecret "github.com/openshift/library-go/pkg/operator/encryption/secrets"
	encryptionstate "github.com/openshift/library-go/pkg/operator/encryption/state"
	"github.com/openshift/library-go/pkg/operator/events"
)

func TestOAuthAPIServerController(t *testing.T) {
	scenarios := []struct {
		name           string
		initialSecrets []*corev1.Secret
		validateFunc   func(ts *testing.T, actions []clientgotesting.Action)

		expectedActions []string
		expectedEvents  []string
	}{
		{
			name:            "test case 1 - the secret doesn't exist and encryption is on",
			initialSecrets:  []*corev1.Secret{defaultSecret(fmt.Sprintf("%s-openshift-apiserver", encryptionconfig.EncryptionConfSecretName))},
			expectedActions: []string{"create:secrets:openshift-config-managed:encryption-config-oauth-apiserver"},
			expectedEvents:  []string{"SecretCreated"},
			validateFunc: func(ts *testing.T, actions []clientgotesting.Action) {
				wasSecretValidated := false
				for _, action := range actions {
					if action.Matches("create", "secrets") {
						createAction := action.(clientgotesting.UpdateAction)
						actualSecret := createAction.GetObject().(*corev1.Secret)

						expectedSecret := defaultSecret(fmt.Sprintf("%s-oauth-apiserver", encryptionconfig.EncryptionConfSecretName))

						if !equality.Semantic.DeepEqual(actualSecret, expectedSecret) {
							ts.Errorf(diff.ObjectDiff(actualSecret, expectedSecret))
						}
						wasSecretValidated = true
						break
					}
				}
				if !wasSecretValidated {
					ts.Errorf("the secret wasn't validated")
				}
			},
		},
		{
			name: "test case 2 - the secret exists, is annotated and it's up to date",
			initialSecrets: []*corev1.Secret{
				defaultSecret(fmt.Sprintf("%s-openshift-apiserver", encryptionconfig.EncryptionConfSecretName)),
				func() *corev1.Secret {
					s := defaultSecret(fmt.Sprintf("%s-oauth-apiserver", encryptionconfig.EncryptionConfSecretName))
					s.Annotations["encryption.apiserver.operator.openshift.io/managed-by"] = encryptionConfigManagedByValue
					return s
				}(),
			},
		},
		{
			name: "test case 2 - the secret exists, is annotated but it's out of date",
			initialSecrets: []*corev1.Secret{
				func() *corev1.Secret {
					s := defaultSecret(fmt.Sprintf("%s-openshift-apiserver", encryptionconfig.EncryptionConfSecretName))
					s.Data["encryption-config"] = []byte{0xAA}
					return s
				}(),
				func() *corev1.Secret {
					s := defaultSecret(fmt.Sprintf("%s-oauth-apiserver", encryptionconfig.EncryptionConfSecretName))
					s.Annotations["encryption.apiserver.operator.openshift.io/managed-by"] = encryptionConfigManagedByValue
					return s
				}(),
			},
			expectedActions: []string{"update:secrets:openshift-config-managed:encryption-config-oauth-apiserver"},
			expectedEvents:  []string{"SecretUpdated"},
			validateFunc: func(ts *testing.T, actions []clientgotesting.Action) {
				wasSecretValidated := false
				for _, action := range actions {
					if action.Matches("update", "secrets") {
						updateAction := action.(clientgotesting.UpdateAction)
						actualSecret := updateAction.GetObject().(*corev1.Secret)

						expectedSecret := defaultSecret(fmt.Sprintf("%s-oauth-apiserver", encryptionconfig.EncryptionConfSecretName))
						expectedSecret.Data["encryption-config"] = []byte{0xAA}

						if !equality.Semantic.DeepEqual(actualSecret, expectedSecret) {
							ts.Errorf(diff.ObjectDiff(actualSecret, expectedSecret))
						}
						wasSecretValidated = true
						break
					}
				}
				if !wasSecretValidated {
					ts.Errorf("the secret wasn't validated")
				}
			},
		},
		{
			name: "test case 3 - no-op the secret was created by CAO in 4.6 and this is downgrade",
			initialSecrets: []*corev1.Secret{
				defaultSecret(fmt.Sprintf("%s-openshift-apiserver", encryptionconfig.EncryptionConfSecretName)),
				defaultSecret(fmt.Sprintf("%s-oauth-apiserver", encryptionconfig.EncryptionConfSecretName)),
			},
		},
		{
			name: "test case 4 - no-op encryption off",
		},
	}

	for _, scenario := range scenarios {
		t.Run(scenario.name, func(t *testing.T) {
			// test data
			eventRecorder := events.NewInMemoryRecorder("")
			syncContext := factory.NewSyncContext("", eventRecorder)
			fakeSecretsIndexer := cache.NewIndexer(cache.MetaNamespaceKeyFunc, cache.Indexers{})
			for _, secret := range scenario.initialSecrets {
				fakeSecretsIndexer.Add(secret)
			}
			fakeSecretsLister := corev1listers.NewSecretLister(fakeSecretsIndexer)

			rawSecrets := []runtime.Object{}
			for _, secret := range scenario.initialSecrets {
				rawSecrets = append(rawSecrets, secret)
			}
			fakeKubeClient := fake.NewSimpleClientset(rawSecrets...)

			target := oauthEncryptionConfigSyncController{
				oauthAPIServerTargetNamespace: "oauth-apiserver",
				secretLister:                  fakeSecretsLister.Secrets(operatorclient.GlobalMachineSpecifiedConfigNamespace),
				secretClient:                  fakeKubeClient.CoreV1().Secrets(operatorclient.GlobalMachineSpecifiedConfigNamespace),
			}

			// act
			err := target.sync(context.TODO(), syncContext)
			if err != nil {
				t.Fatal(err)
			}

			// validate
			if err := validateActionsVerbs(fakeKubeClient.Actions(), scenario.expectedActions); err != nil {
				t.Fatal(err)
			}

			if err := validateEventsReason(eventRecorder.Events(), scenario.expectedEvents); err != nil {
				t.Error(err)
			}
			if scenario.validateFunc != nil {
				scenario.validateFunc(t, fakeKubeClient.Actions())
			}
		})
	}
}

func defaultSecret(name string) *corev1.Secret {
	return &corev1.Secret{
		ObjectMeta: v1.ObjectMeta{
			Name:      name,
			Namespace: operatorclient.GlobalMachineSpecifiedConfigNamespace,
			Annotations: map[string]string{
				EncryptionConfigManagedBy:                encryptionConfigManagedByValue,
				encryptionstate.KubernetesDescriptionKey: encryptionstate.KubernetesDescriptionScaryValue,
			},
			Finalizers: []string{encryptionsecret.EncryptionSecretFinalizer},
		},
		Data: map[string][]byte{"encryption-config": {0xFF}},
	}
}

func validateActionsVerbs(actualActions []clientgotesting.Action, expectedActions []string) error {
	if len(actualActions) != len(expectedActions) {
		return fmt.Errorf("expected to get %d actions but got %d\nexpected=%v \n got=%v", len(expectedActions), len(actualActions), expectedActions, actionStrings(actualActions))
	}
	for i, a := range actualActions {
		if got, expected := actionString(a), expectedActions[i]; got != expected {
			return fmt.Errorf("at %d got %s, expected %s", i, got, expected)
		}
	}
	return nil
}

func actionString(a clientgotesting.Action) string {
	involvedObjectName := ""
	if updateAction, isUpdateAction := a.(clientgotesting.UpdateAction); isUpdateAction {
		rawObj := updateAction.GetObject()
		if objMeta, err := meta.Accessor(rawObj); err == nil {
			involvedObjectName = objMeta.GetName()
		}
	}
	if getAction, isGetAction := a.(clientgotesting.GetAction); isGetAction {
		involvedObjectName = getAction.GetName()
	}
	action := a.GetVerb() + ":" + a.GetResource().Resource
	if len(a.GetNamespace()) > 0 {
		action = action + ":" + a.GetNamespace()
	}
	if len(involvedObjectName) > 0 {
		action = action + ":" + involvedObjectName
	}
	return action
}

func actionStrings(actions []clientgotesting.Action) []string {
	res := make([]string, 0, len(actions))
	for _, a := range actions {
		res = append(res, actionString(a))
	}
	return res
}

func validateEventsReason(actualEvents []*corev1.Event, expectedReasons []string) error {
	if len(actualEvents) != len(expectedReasons) {
		return fmt.Errorf("expected to get %d events but got %d\nexpected=%v \n got=%v", len(expectedReasons), len(actualEvents), expectedReasons, eventReasons(actualEvents))
	}
	for i, e := range actualEvents {
		if got, expected := e.Reason, expectedReasons[i]; got != expected {
			return fmt.Errorf("at %d got %s, expected %s", i, got, expected)
		}
	}
	return nil
}

func eventReasons(events []*corev1.Event) []string {
	ret := make([]string, 0, len(events))
	for _, ev := range events {
		ret = append(ret, ev.Reason)
	}
	return ret
}
