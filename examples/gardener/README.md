# Gardener Provider for multicluster-runtime

This repository provides a [Gardener](https://gardener.cloud) provider implementation for the multicluster-runtime project.
The provider enables integration with Gardener's multi-cluster management capabilities, supporting two distinct flavors: **garden** and **seed**.

## Overview

The Gardener provider facilitates interaction with Gardener-managed clusters by watching specific resources and managing short-lived, auto-renewed kubeconfigs for secure access.
It supports two operational modes:

### `garden` Flavor

- **Functionality**: The controller communicates with the **garden cluster** and monitors `core.gardener.cloud/v1beta1.Shoot` resources.
- **Authentication**: Requests temporary [**admin kubeconfigs**](https://gardener.cloud/docs/gardener/shoot/shoot_access/) that are short-lived and automatically renewed for secure access to Shoot clusters.
- **Use Case**: Ideal for managing Shoot resources directly within the garden cluster.

### `seed` Flavor

- **Functionality**: The controller connects to a **seed cluster** and monitors `extensions.gardener.cloud/v1alpha1.Cluster` resources.
- **Authentication**: Utilizes the standard **cluster-admin kubeconfig** provided by the `gardenlet`, which is also short-lived and auto-renewed.
- **Use Case**: Suitable for managing Cluster resources within a seed cluster environment.

## Getting Started

To use the Gardener provider, ensure you have a running Gardener setup and the necessary permissions to access garden and/or seed clusters.
Detailed setup and configuration instructions can be found [here](https://gardener.cloud/docs/gardener/deployment/getting_started_locally/).
