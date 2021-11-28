# AWS Load Balancer Drain Wait

This repository holds a simple golang app to be used in a `preStop`
hook in pods targeted in IP mode by the AWS Load Balancer Controller.

It works by fetching all the ingresses in the current namespace, gets
load balancer hostnames, and then polls the AWS ELBv2 API until the pod's
IP address is no longer in a healthy state in any of those load balancers.

## TODOs

* Figure out compatibility matrix.

## Requirements

### AWS Permissions

This app needs the following IAM permissions - TODO.

```json
```

### Kubernetes RBAC

This app also needs to be able to get ingress objects from
each namespace you intend to use it in. An example RBAC config is found
in the `deploy` folder.

## Testing Locally

TODO
