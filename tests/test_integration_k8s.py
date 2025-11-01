#!/usr/bin/env python3

"""Integration tests for Kubernetes API interactions.

These tests require a Kubernetes cluster (kind, minikube, or real cluster).
They test the actual API interactions, not mocked responses.
"""

import pytest
from kubernetes import client, config as k8s_config
from kubernetes.client.rest import ApiException


pytestmark = pytest.mark.integration


class TestKubernetesConnectivity:
    """Tests for basic Kubernetes connectivity."""

    @pytest.fixture(scope="class", autouse=True)
    def setup_k8s(self):
        """Setup Kubernetes client for tests."""
        try:
            k8s_config.load_kube_config()
        except Exception:
            pytest.skip("No Kubernetes cluster available")

    def test_api_server_reachable(self):
        """Kubernetes API server should be reachable."""
        v1 = client.CoreV1Api()
        # Simple API call to verify connectivity
        namespaces = v1.list_namespace()
        assert namespaces is not None
        assert len(namespaces.items) > 0

    def test_custom_resources_api_available(self):
        """Custom resources API should be available."""
        api = client.CustomObjectsApi()
        assert api is not None


class TestMachineResourceOperations:
    """Integration tests for Machine custom resource operations."""

    @pytest.fixture(scope="class", autouse=True)
    def setup_k8s(self):
        """Setup Kubernetes client for tests."""
        try:
            k8s_config.load_kube_config()
        except Exception:
            pytest.skip("No Kubernetes cluster available")

    @pytest.fixture(scope="class")
    def test_namespace(self):
        """Create test namespace for integration tests."""
        v1 = client.CoreV1Api()
        namespace_name = "nio-integration-tests"

        # Create namespace
        namespace = client.V1Namespace(
            metadata=client.V1ObjectMeta(name=namespace_name)
        )
        try:
            v1.create_namespace(namespace)
        except ApiException as e:
            if e.status != 409:  # Already exists
                raise

        yield namespace_name

        # Cleanup: Delete namespace
        try:
            v1.delete_namespace(namespace_name)
        except ApiException:
            pass  # Ignore cleanup errors

    @pytest.mark.skipif(
        True, reason="Requires CRDs to be installed"
    )  # Skip by default
    def test_create_machine_resource(self, test_namespace):
        """Should be able to create a Machine custom resource."""
        api = client.CustomObjectsApi()

        machine = {
            "apiVersion": "nio.homystack.com/v1alpha1",
            "kind": "Machine",
            "metadata": {
                "name": "test-machine",
                "namespace": test_namespace,
            },
            "spec": {
                "hostname": "test.example.com",
                "username": "root",
                "credentialsRef": {"name": "test-creds"},
            },
        }

        try:
            api.create_namespaced_custom_object(
                group="nio.homystack.com",
                version="v1alpha1",
                namespace=test_namespace,
                plural="machines",
                body=machine,
            )
        except ApiException as e:
            # CRD might not be installed
            if e.status == 404:
                pytest.skip("Machine CRD not installed")
            raise

        # Verify creation
        created = api.get_namespaced_custom_object(
            group="nio.homystack.com",
            version="v1alpha1",
            namespace=test_namespace,
            plural="machines",
            name="test-machine",
        )
        assert created["metadata"]["name"] == "test-machine"

        # Cleanup
        api.delete_namespaced_custom_object(
            group="nio.homystack.com",
            version="v1alpha1",
            namespace=test_namespace,
            plural="machines",
            name="test-machine",
        )


class TestNixOSConfigurationResourceOperations:
    """Integration tests for NixOSConfiguration custom resource operations."""

    @pytest.fixture(scope="class", autouse=True)
    def setup_k8s(self):
        """Setup Kubernetes client for tests."""
        try:
            k8s_config.load_kube_config()
        except Exception:
            pytest.skip("No Kubernetes cluster available")

    @pytest.fixture(scope="class")
    def test_namespace(self):
        """Create test namespace for integration tests."""
        v1 = client.CoreV1Api()
        namespace_name = "nio-integration-tests"

        # Create namespace
        namespace = client.V1Namespace(
            metadata=client.V1ObjectMeta(name=namespace_name)
        )
        try:
            v1.create_namespace(namespace)
        except ApiException as e:
            if e.status != 409:  # Already exists
                raise

        yield namespace_name

        # Cleanup
        try:
            v1.delete_namespace(namespace_name)
        except ApiException:
            pass

    @pytest.mark.skipif(
        True, reason="Requires CRDs to be installed"
    )  # Skip by default
    def test_create_nixos_configuration(self, test_namespace):
        """Should be able to create a NixOSConfiguration custom resource."""
        api = client.CustomObjectsApi()

        config = {
            "apiVersion": "nio.homystack.com/v1alpha1",
            "kind": "NixOSConfiguration",
            "metadata": {
                "name": "test-config",
                "namespace": test_namespace,
            },
            "spec": {
                "machineRef": {"name": "test-machine"},
                "gitRepo": "https://github.com/example/nixos-config.git",
                "flakePath": ".#hostname",
            },
        }

        try:
            api.create_namespaced_custom_object(
                group="nio.homystack.com",
                version="v1alpha1",
                namespace=test_namespace,
                plural="nixosconfigurations",
                body=config,
            )
        except ApiException as e:
            if e.status == 404:
                pytest.skip("NixOSConfiguration CRD not installed")
            raise

        # Verify creation
        created = api.get_namespaced_custom_object(
            group="nio.homystack.com",
            version="v1alpha1",
            namespace=test_namespace,
            plural="nixosconfigurations",
            name="test-config",
        )
        assert created["metadata"]["name"] == "test-config"

        # Cleanup
        api.delete_namespaced_custom_object(
            group="nio.homystack.com",
            version="v1alpha1",
            namespace=test_namespace,
            plural="nixosconfigurations",
            name="test-config",
        )


class TestSecretOperations:
    """Integration tests for Secret operations."""

    @pytest.fixture(scope="class", autouse=True)
    def setup_k8s(self):
        """Setup Kubernetes client for tests."""
        try:
            k8s_config.load_kube_config()
        except Exception:
            pytest.skip("No Kubernetes cluster available")

    @pytest.fixture(scope="class")
    def test_namespace(self):
        """Create test namespace for integration tests."""
        v1 = client.CoreV1Api()
        namespace_name = "nio-integration-tests"

        namespace = client.V1Namespace(
            metadata=client.V1ObjectMeta(name=namespace_name)
        )
        try:
            v1.create_namespace(namespace)
        except ApiException as e:
            if e.status != 409:
                raise

        yield namespace_name

        try:
            v1.delete_namespace(namespace_name)
        except ApiException:
            pass

    def test_create_and_read_secret(self, test_namespace):
        """Should be able to create and read secrets."""
        v1 = client.CoreV1Api()

        secret = client.V1Secret(
            metadata=client.V1ObjectMeta(name="test-secret"),
            string_data={"ssh-privatekey": "test-key-content"},
        )

        # Create secret
        v1.create_namespaced_secret(test_namespace, secret)

        # Read secret
        read_secret = v1.read_namespaced_secret("test-secret", test_namespace)
        assert read_secret.metadata.name == "test-secret"
        assert "ssh-privatekey" in read_secret.data

        # Cleanup
        v1.delete_namespaced_secret("test-secret", test_namespace)
