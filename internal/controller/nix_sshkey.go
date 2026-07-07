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
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"fmt"

	"golang.org/x/crypto/ssh"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
)

// Secret data keys for the remote-build SSH keypair a NixStore owns. The private
// key lets the NixBuilder push to the store and lets runner pods dispatch builds
// to the builder; the public key is the authorized_keys entry on the store's and
// builder's sshd (design: delegated remote build, ADR-0008).
const (
	sshSecretPrivateKey = "ssh-privatekey"     // OpenSSH ed25519 private key
	sshSecretPublicKey  = "ssh-authorized-key" // "ssh-ed25519 AAAA..." single line

	// sshVolumeName carries the SSH keypair Secret into store/builder/runner pods.
	sshVolumeName = "nio-ssh"

	// sshKeyMountPath is where the SSH keypair Secret is mounted.
	sshKeyMountPath = "/etc/nio/ssh"

	// sshPrivateKeyPath is the mounted private key path used by nix over ssh-ng.
	sshPrivateKeyPath = sshKeyMountPath + "/" + sshSecretPrivateKey
)

// sshSecretName returns the name of the SSH keypair Secret owned by a store.
func sshSecretName(storeName string) string {
	return storeName + "-ssh"
}

// sshdBringUp returns shell that starts an SSH server accepting the shared
// remote-build key (authorized_keys must already be at /root/.ssh). It uses
// dropbear rather than OpenSSH's sshd: OpenSSH needs a dedicated 'sshd'
// privilege-separation user, but the nix image's /etc/passwd is a read-only
// symlink into the store, so that user cannot be added. dropbear needs no such
// user. The nix remote-build client still uses the OpenSSH `ssh` binary; the
// protocols are compatible. Starts foreground when fg, else backgrounded.
func sshdBringUp(fg bool) string {
	launch := "dropbear -F -E -s -p 22 -r /etc/dropbear/hostkey"
	if fg {
		launch = "exec " + launch
	}
	amp := " &"
	if fg {
		amp = ""
	}
	return `mkdir -p /etc/dropbear /root/.ssh
chmod 700 /root/.ssh
# dropbear rejects a login whose shell is not an accepted user shell; publish
# root's shell (from the image passwd) via /etc/shells so getusershell accepts
# it. Use shell builtins only — the bare nix image has no grep/awk on PATH.
: > /etc/shells 2>/dev/null || true
while IFS=: read -r u _ _ _ _ _ sh; do [ "$u" = root ] && [ -n "$sh" ] && echo "$sh" >> /etc/shells; done < /etc/passwd 2>/dev/null || true
echo /bin/sh >> /etc/shells 2>/dev/null || true
nix shell nixpkgs#dropbear --command sh -c 'dropbearkey -t ed25519 -f /etc/dropbear/hostkey >/dev/null 2>&1 || true; ` + launch + `'` + amp + `
`
}

// generateOpenSSHKeyPair creates an ed25519 keypair and returns the OpenSSH
// private key (PEM) and the authorized_keys public-key line.
func generateOpenSSHKeyPair(comment string) (privatePEM, authorizedKey string, err error) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return "", "", fmt.Errorf("generating ed25519 key: %w", err)
	}
	block, err := ssh.MarshalPrivateKey(priv, comment)
	if err != nil {
		return "", "", fmt.Errorf("marshaling private key: %w", err)
	}
	sshPub, err := ssh.NewPublicKey(pub)
	if err != nil {
		return "", "", fmt.Errorf("building ssh public key: %w", err)
	}
	return string(pem.EncodeToMemory(block)), string(ssh.MarshalAuthorizedKey(sshPub)), nil
}

// ensureSSHKeySecret ensures the store's SSH keypair Secret exists (generating it
// once) and returns the authorized_keys public line for wiring sshd.
func ensureSSHKeySecret(ctx context.Context, c client.Client, scheme *runtime.Scheme, owner client.Object, storeName string) (string, error) {
	name := sshSecretName(storeName)
	var secret corev1.Secret
	err := c.Get(ctx, client.ObjectKey{Namespace: owner.GetNamespace(), Name: name}, &secret)
	if err == nil {
		if pub, ok := secret.Data[sshSecretPublicKey]; ok && len(pub) > 0 {
			return string(pub), nil
		}
		return "", fmt.Errorf("ssh secret %q missing %q", name, sshSecretPublicKey)
	}
	if !apierrors.IsNotFound(err) {
		return "", fmt.Errorf("getting ssh secret: %w", err)
	}

	priv, pub, genErr := generateOpenSSHKeyPair("nio-remote-build")
	if genErr != nil {
		return "", genErr
	}
	secret = corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: owner.GetNamespace(),
			Labels:    managedLabels("NixStore", storeName),
		},
		Type: corev1.SecretTypeOpaque,
		Data: map[string][]byte{
			sshSecretPrivateKey: []byte(priv),
			sshSecretPublicKey:  []byte(pub),
		},
	}
	if err := controllerutil.SetControllerReference(owner, &secret, scheme); err != nil {
		return "", err
	}
	if err := c.Create(ctx, &secret); err != nil {
		if apierrors.IsAlreadyExists(err) {
			if getErr := c.Get(ctx, client.ObjectKey{Namespace: owner.GetNamespace(), Name: name}, &secret); getErr == nil {
				return string(secret.Data[sshSecretPublicKey]), nil
			}
		}
		return "", fmt.Errorf("creating ssh secret: %w", err)
	}
	return pub, nil
}
