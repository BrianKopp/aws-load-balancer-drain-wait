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

```sh
go run . --aws-profile default --aws-region us-east-1 --kubeconfig ~/.kube/config
```

## Consuming the Service

Invoke the drain-delay by making the following HTTP request.

```sh
curl "http://<address-to-service>:8080/drain-delay?max-delay=60&ip=<YOUR_IP>&namespace=default&ingress=<ingress_name>"
```

Here the query parameters are:

* `max-delay` - the maximum amount of time, in seconds for the service to poll the AWS ELB API
* `ip` - the IP address of the target to wait to be drained
* `namespace` - the namespace of the ingress associated with the load balancer to check
* `ingress` - the name of the ingress associated with the load balancer to check

### Consuming as part of a pre-stop hook

Kubernetes supports pre-stop hooks which allow injecting logic after the pod is
marked as terminating, but before your container receives a SIGTERM.

The AWS Load Balancer Controller watches for pods getting put in a Terminating
state. When this happens, it makes a deregistration API call to the AWS
ELB API. You can use this service to add a dynamic-timed delay *prior*
to your application receiving a SIGTERM.

Add the following pre-stop hook configuration.

```yaml
# Pod spec
spec:
  containers:
  - # Other config
    lifecycle:
      preStop:
        exec:
          command:
            - "/bin/sh"
            - "-c"
            - "/usr/bin/prestop.sh"
```

And in `/usr/bin/prestop.sh`, or whatever you want to call it

```sh
STATUS_CODE=$(curl -s -o /dev/null -w "%{http_code}" "http://<domain>:8080/drain-delay?max-wait=60&ip=$MY_IP&namespaace=<namespace>&ingress=<ingress>")
if [[ "$STATUS_CODE" -eq "200" ]]; then
    exit 0
fi
echo "something bad must have happened, go ahead and manually wait the max-wait"
sleep 60
exit 0
```
