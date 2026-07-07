/*
Copyright 2026.

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
	"strings"
	"testing"

	"golang.org/x/crypto/ssh"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	niov1alpha1 "github.com/kitsunoff/nixos-operator/api/v1alpha1"
)

func TestGenerateOpenSSHKeyPair(t *testing.T) {
	priv, pub, err := generateOpenSSHKeyPair("test")
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	// Private key must parse as an OpenSSH key.
	if _, err := ssh.ParsePrivateKey([]byte(priv)); err != nil {
		t.Errorf("private key does not parse: %v", err)
	}
	// Public key must be a valid single authorized_keys line.
	if !strings.HasPrefix(pub, "ssh-ed25519 ") {
		t.Errorf("unexpected public key form: %q", pub)
	}
	if _, _, _, _, err := ssh.ParseAuthorizedKey([]byte(pub)); err != nil {
		t.Errorf("authorized key does not parse: %v", err)
	}
}

func TestEnsureSSHKeySecret(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := niov1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("scheme: %v", err)
	}
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatalf("corev1 scheme: %v", err)
	}
	store := &niov1alpha1.NixStore{ObjectMeta: metav1.ObjectMeta{Name: "store", Namespace: "apps"}}
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(store).Build()

	pub1, err := ensureSSHKeySecret(context.Background(), c, scheme, store, "store")
	if err != nil {
		t.Fatalf("ensure: %v", err)
	}
	if !strings.HasPrefix(pub1, "ssh-ed25519 ") {
		t.Errorf("bad public key: %q", pub1)
	}

	// Idempotent: a second call returns the same key without regenerating.
	pub2, err := ensureSSHKeySecret(context.Background(), c, scheme, store, "store")
	if err != nil {
		t.Fatalf("ensure #2: %v", err)
	}
	if pub1 != pub2 {
		t.Error("ensureSSHKeySecret regenerated the key on the second call")
	}

	var secret corev1.Secret
	if err := c.Get(context.Background(), client.ObjectKey{Namespace: "apps", Name: sshSecretName("store")}, &secret); err != nil {
		t.Fatalf("get secret: %v", err)
	}
	if _, ok := secret.Data[sshSecretPrivateKey]; !ok {
		t.Error("secret missing private key")
	}
}
