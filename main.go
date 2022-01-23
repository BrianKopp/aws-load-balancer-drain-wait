package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"sync"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	elbv2 "github.com/aws/aws-sdk-go-v2/service/elasticloadbalancingv2"
	elbv2types "github.com/aws/aws-sdk-go-v2/service/elasticloadbalancingv2/types"

	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
)

func main() {
	log.Print("Starting golang server\n")
	appConfig := buildConfigFromFlags()

	// setup clients
	var k8sConfig *rest.Config
	var err error
	if appConfig.KubeConfigFile != "" {
		k8sConfig, err = clientcmd.BuildConfigFromFlags("", appConfig.KubeConfigFile)
	} else {
		k8sConfig, err = rest.InClusterConfig()
	}
	if err != nil {
		exitWithError(err)
	}

	k8sClientSet, err := kubernetes.NewForConfig(k8sConfig)
	if err != nil {
		exitWithError(err)
	}

	// get the load balancer from aws
	var awsConfig aws.Config
	if appConfig.AWSProfile != "" {
		log.Printf("Using AWS Profile %v", appConfig.AWSProfile)
		awsConfig, err = config.LoadDefaultConfig(context.Background(),
			config.WithSharedConfigProfile(appConfig.AWSProfile),
			config.WithRegion(appConfig.AWSRegion),
			config.WithSharedConfigFiles(config.DefaultSharedConfigFiles),
			config.WithSharedCredentialsFiles(config.DefaultSharedCredentialsFiles))
	} else if appConfig.AWSRegion != "" {
		log.Printf("Using AWS Region %v", appConfig.AWSRegion)
		awsConfig, err = config.LoadDefaultConfig(context.Background(),
			config.WithRegion(appConfig.AWSRegion))
	} else {
		log.Printf("Using default aws config")
		awsConfig, err = config.LoadDefaultConfig(context.Background())
	}
	if err != nil {
		exitWithError(err)
	}

	elbClient := elbv2.NewFromConfig(awsConfig)

	drainClient := &DrainDelayClient{
		elbv2Client: elbClient,
		k8sClient:   k8sClientSet,
	}

	// create HTTP server
	server := &http.Server{Addr: ":8080"}

	http.HandleFunc("/health", func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, "OK")
	})

	http.HandleFunc("/drain-delay", func(w http.ResponseWriter, r *http.Request) {
		delayConfig, err := buildConfigFromRequest(r)
		if err != nil {
			log.Printf("error building config from request: %v\n", err)
			http.Error(w, "Bad request", http.StatusBadRequest)
			return
		}
		err = drainClient.DelayUntilDrain(delayConfig)
		if err != nil {
			log.Printf("error delaying until drain: %v\n", err)
			http.Error(w, "Drain delay failed", http.StatusInternalServerError)
			return
		}
		fmt.Fprintf(w, "Success")
	})

	wg := &sync.WaitGroup{}
	wg.Add(1)

	go func() {
		defer wg.Done()

		// always returns error. ErrServerClosed on graceful close
		if err := server.ListenAndServe(); err != http.ErrServerClosed {
			// unexpected error
			log.Fatalf("ListenAndServe(): %v", err)
		}
	}()

	log.Println("Listening on port 8080")
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt)

	<-stop
	log.Println("Signal received, closing server")

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	if err := server.Shutdown(ctx); err != nil {
		log.Printf("Error in server.Shutdown() %v", err)
		os.Exit(1)
	}

	log.Println("Successfully closed server")
	os.Exit(0)
}

func exitWithError(err error) {
	fmt.Fprintln(os.Stderr, err)
	os.Exit(1)
}

type AppConfig struct {
	KubeConfigFile string
	AWSRegion      string
	AWSProfile     string
	Port           int
}

func buildConfigFromFlags() AppConfig {
	appConfig := AppConfig{}
	flag.StringVar(&appConfig.KubeConfigFile, "kubeconfig", "", "The path for the kubeconfig file to use")
	flag.StringVar(&appConfig.AWSRegion, "aws-region", "", "The AWS region")
	flag.StringVar(&appConfig.AWSProfile, "aws-profile", "", "The AWS profile to use")
	flag.IntVar(&appConfig.Port, "port", 8080, "port to listen on")
	flag.Parse()
	return appConfig
}

type DelayConfig struct {
	MaxDelay         int    // optional, defaullt 60s
	IPAddress        string // required, needed to look up self in target groups
	IngressNamespace string // required, namespaace of ingress resource
	IngressName      string // required, the name of the ingress to check against
}

type DrainDelayClient struct {
	elbv2Client *elbv2.Client
	k8sClient   *kubernetes.Clientset
}

func buildConfigFromRequest(r *http.Request) (*DelayConfig, error) {
	delayConfig := &DelayConfig{}

	maxDelayStr := r.URL.Query().Get("max-delay")
	if maxDelayStr == "" {
		delayConfig.MaxDelay = 60
	} else {
		parsedInt, err := strconv.Atoi(maxDelayStr)
		if err != nil {
			return nil, err
		}
		delayConfig.MaxDelay = parsedInt
	}

	delayConfig.IPAddress = r.URL.Query().Get("ip")
	if delayConfig.IPAddress == "" {
		return nil, errors.New("ip must be in query param")
	}

	delayConfig.IngressNamespace = r.URL.Query().Get("namespace")
	if delayConfig.IngressNamespace == "" {
		return nil, errors.New("namespace must be in query param")
	}

	delayConfig.IngressName = r.URL.Query().Get("ingress")
	if delayConfig.IngressName == "" {
		return nil, errors.New("ingress must be in query param")
	}

	return delayConfig, nil
}

func waitUntil(ctx context.Context) {
	<-ctx.Done()
}

func (c *DrainDelayClient) DelayUntilDrain(delayConfig *DelayConfig) error {
	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(delayConfig.MaxDelay)*time.Second)
	defer cancel()

	log.Printf("Beginning drain delay for IP %s for ingress %s / %s with max delay %v", delayConfig.IPAddress, delayConfig.IngressNamespace, delayConfig.IngressName, delayConfig.MaxDelay)

	ingress, err := c.k8sClient.NetworkingV1().Ingresses(delayConfig.IngressNamespace).Get(ctx, delayConfig.IngressName, v1.GetOptions{})
	if err != nil {
		log.Printf("Error getting ingress from kubernetes api '%s'\n", err)
		waitUntil(ctx)
		return err
	}

	hostname := ""
	for _, lbIngress := range ingress.Status.LoadBalancer.Ingress {
		if lbIngress.Hostname != "" {
			hostname = lbIngress.Hostname
		}
	}

	if hostname == "" {
		log.Printf("No hostname for ingress %s/%s", delayConfig.IngressNamespace, delayConfig.IngressName)
		return errors.New(fmt.Sprintf("No hostname for ingress %s/%s", delayConfig.IngressNamespace, delayConfig.IngressName))
	}

	ingressElbArn, err := c.GetArnForELBWithHostname(ctx, hostname)
	if err != nil {
		log.Printf("Error getting ELB ARN for hostname %s - %s\n", hostname, err)
		waitUntil(ctx)
		return err
	}
	if ingressElbArn == "" {
		return errors.New(fmt.Sprintf("could not find ingress ELB ARN for hostname %s", hostname))
	}

	remainingTargetGroupArns, err := c.GetTargetGroupsForELB(ctx, ingressElbArn)
	if err != nil {
		log.Printf("Error getting target group arns for ELB - %v", err)
		waitUntil(ctx)
		return err
	}

	return c.WaitUntilIPDraining(ctx, delayConfig.IPAddress, remainingTargetGroupArns)
}

func (c *DrainDelayClient) GetArnForELBWithHostname(ctx context.Context, hostname string) (string, error) {
	for {
		if ctx.Err() != nil {
			return "", ctx.Err()
		}

		elbDescribeResponse, err := c.elbv2Client.DescribeLoadBalancers(ctx, &elbv2.DescribeLoadBalancersInput{})
		if err != nil {
			log.Printf("error getting elbs %s\n", err)
			time.Sleep(100 * time.Millisecond)
			continue
		}

		for _, elb := range elbDescribeResponse.LoadBalancers {
			if *elb.DNSName == hostname {
				return *elb.LoadBalancerArn, nil
			}
		}

		log.Printf("could not find ELB with hostname %s", hostname)
		return "", nil
	}
}

func (c *DrainDelayClient) GetTargetGroupsForELB(ctx context.Context, elbArn string) ([]string, error) {
	for {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}

		descTargetGroupResp, err := c.elbv2Client.DescribeTargetGroups(ctx, &elbv2.DescribeTargetGroupsInput{LoadBalancerArn: &elbArn})
		if err != nil {
			log.Printf("error getting target groups %s\n", err)
			time.Sleep(100 * time.Millisecond)
			continue
		}

		targetGroupArns := []string{}
		for _, tg := range descTargetGroupResp.TargetGroups {
			if tg.TargetType != elbv2types.TargetTypeEnumIp {
				continue
			}
			targetGroupArns = append(targetGroupArns, *tg.TargetGroupArn)
		}

		log.Printf("Found %v target groups with IP targets for ELB %s", len(targetGroupArns), elbArn)
		return targetGroupArns, nil
	}
}

func (c *DrainDelayClient) WaitUntilIPDraining(ctx context.Context, ip string, targetGroupArns []string) error {
	stillHealthyTargetGroupARNs := []string{}
	for {
		log.Printf("Searching %v target groups to see if ip %v is draining", len(targetGroupArns), ip)
		for _, tg := range targetGroupArns {
			if ctx.Err() != nil {
				log.Printf("Timed out waiting for IP %s to be draining from target groups", ip)
				return ctx.Err()
			}

			targets, err := c.elbv2Client.DescribeTargetHealth(ctx, &elbv2.DescribeTargetHealthInput{
				TargetGroupArn: &tg,
			})

			if err != nil {
				fmt.Println(err)
				stillHealthyTargetGroupARNs = append(stillHealthyTargetGroupARNs, tg)
				continue
			}

			for _, target := range targets.TargetHealthDescriptions {
				isMyIP := *target.Target.Id == ip
				inDrainingState := target.TargetHealth.State == elbv2types.TargetHealthStateEnumDraining || target.TargetHealth.State == elbv2types.TargetHealthStateEnumUnused
				if isMyIP && !inDrainingState {
					stillHealthyTargetGroupARNs = append(stillHealthyTargetGroupARNs, tg)
					log.Printf("Found %s in target group %s in state %s", ip, tg, target.TargetHealth.State)
					break
				}
			}
		}

		if len(stillHealthyTargetGroupARNs) == 0 {
			log.Printf("No more target groups using IP %v", ip)
			return nil
		}

		targetGroupArns = make([]string, len(stillHealthyTargetGroupARNs))
		copy(targetGroupArns, stillHealthyTargetGroupARNs)
		stillHealthyTargetGroupARNs = []string{}
		time.Sleep(250 * time.Millisecond)
	}
}
