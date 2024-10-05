package main

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	ec2Types "github.com/aws/aws-sdk-go-v2/service/ec2/types"
	"github.com/aws/aws-sdk-go-v2/service/ec2instanceconnect"
	"github.com/aws/aws-sdk-go-v2/service/ssm"
	"log/slog"
	"os"
	"os/exec"
	"os/signal"
	"syscall"
	"time"
)

type Config struct {
	AppHome      string `json:"-"`
	AwsProfile   string `json:"-"`
	Region       string `json:"region"`
	InstanceName string `json:"-"`
	InstanceID   string `json:"instance_id"`
	InstanceAZ   string `json:"instance_az"`
	InstanceUser string `json:"-"`
}

var cfg Config
var awsConfig aws.Config

func main() {
	if len(os.Args) != 4 {
		fmt.Fprintf(os.Stderr, "Usage: %s <aws-profile> <instance-name> <instance-user>\n", os.Args[0])
		os.Exit(1)
	}
	cfg.AwsProfile = os.Args[1]
	cfg.InstanceName = os.Args[2]
	cfg.InstanceUser = os.Args[3]

	cfg.AppHome = os.Getenv("HOME") + "/.ssm-ssh-connect"

	// Set up logging
	err := os.MkdirAll(cfg.AppHome, 0750)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to create app home directory: %v\n", err)
		os.Exit(1)
	}

	// remove log file if its size is greater than 1MB to avoid filling up disk space
	s, err := os.Stat(cfg.AppHome + "/ssm-ssh-connect.log")
	if err == nil && s.Size() > 1024*1024 {
		os.Remove(cfg.AppHome + "/ssm-ssh-connect.log")
	}

	logFile, err := os.OpenFile(cfg.AppHome+"/ssm-ssh-connect.log", os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0660)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to open log file: %v\n", err)
		os.Exit(1)
	}
	defer logFile.Close()

	// create logger
	opts := &slog.HandlerOptions{
		Level: slog.LevelError,
	}
	if os.Getenv("SSM_SSH_CONNECT_DEBUG") == "1" {
		opts.Level = slog.LevelDebug
	}
	logger := slog.New(slog.NewTextHandler(logFile, opts)).With("pid", os.Getpid())
	slog.SetDefault(logger)

	// load AWS configuration
	awsConfig, err = config.LoadDefaultConfig(context.TODO(), config.WithSharedConfigProfile(cfg.AwsProfile))
	if err != nil {
		slog.Error("unable to load AWS config")
	}

	// Handle graceful shutdown
	signal.Notify(shutdown(logFile), syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP)

	// try to load cache
	loadCache(&cfg)
	slog.Info("loaded cache: ", "cfg", cfg)

	// get instance details
	if cfg.InstanceID == "" {
		slog.Info("instance details not found in cache, fetching from AWS")
		// Get the instance ID and region by name
		err = getInstanceDetails()
		if err != nil {
			slog.Error("Failed to get instance details", "error", err)
			os.Exit(1)
		}

		slog.Info("saving instance details to cache")
		saveCache(&cfg)
	}

	// send SSH public key if needed
	lockFileName := fmt.Sprintf(
		"%s/%s-%s-%s.lock",
		cfg.AppHome,
		cfg.AwsProfile,
		cfg.InstanceName,
		cfg.InstanceUser,
	)

	slog.Info("checking lock file " + lockFileName)

	// remove lock file if its older than 50 seconds
	s, err = os.Stat(lockFileName)
	if err == nil && time.Since(s.ModTime()) > 50*time.Second {
		os.Remove(lockFileName)
	}

	if lockFile, err := os.OpenFile(lockFileName, os.O_EXCL|os.O_CREATE, 0660); err == nil {
		// remove lock file on exit
		defer func() {
			lockFile.Close()
			os.Remove(lockFileName)
		}()

		// or remove it after 50 seconds, since public key is valid for 60 seconds only
		go func() {
			time.Sleep(50 * time.Second)
			lockFile.Close()
			os.Remove(lockFileName)
		}()

		// send SSH public key
		slog.Info("sending SSH public key")
		if err := sendSSHPublicKey(); err != nil {
			slog.Error("failed to send SSH public key", "error", err)
		}
		slog.Info("SSH public key sent")
	}

	// Start SSM session
	slog.Info("starting SSM session")
	err = startSSMSessionWithPlugin()
	if err != nil {
		slog.Error("Failed to start SSM session", "error", err)
	}
	slog.Info("session completed")
}

func saveCache(cfg *Config) error {
	cacheFile := fmt.Sprintf(
		"%s/%s-%s-%s.json",
		cfg.AppHome,
		cfg.AwsProfile,
		cfg.InstanceName,
		cfg.InstanceUser,
	)

	data, err := json.Marshal(cfg)
	if err != nil {
		return fmt.Errorf("failed to marshal config: %v", err)
	}

	if err := os.WriteFile(cacheFile, data, 0660); err != nil {
		return fmt.Errorf("failed to write cache file: %v", err)
	}

	return nil
}

func loadCache(cfg *Config) error {
	cacheFile := fmt.Sprintf(
		"%s/%s-%s-%s.json",
		cfg.AppHome,
		cfg.AwsProfile,
		cfg.InstanceName,
		cfg.InstanceUser,
	)

	// check if cache exists
	info, err := os.Stat(cacheFile)
	if err != nil {
		return fmt.Errorf("cache does not exist")
	}

	// ttl
	if time.Since(info.ModTime()) > 24*time.Hour {
		return fmt.Errorf("cache is expired")
	}

	// read cache
	data, err := os.ReadFile(cacheFile)
	if err != nil {
		return fmt.Errorf("failed to read cache file: %v", err)
	}

	// parse cache
	if err := json.Unmarshal(data, cfg); err != nil {
		return fmt.Errorf("failed to unmarshal config: %v", err)
	}

	return nil // cache is valid and loaded
}

func shutdown(logFile *os.File) chan os.Signal {
	signals := make(chan os.Signal, 1)
	go func() {
		s := <-signals
		for s == syscall.SIGHUP {
			slog.Info("received SIGHUP signal: ignoring")
			s = <-signals
		}
		slog.Warn("received shutdown signal: exiting" + s.String())
		logFile.Close()
		os.Exit(0)
	}()

	return signals
}

func getInstanceDetails() error {
	client := ec2.NewFromConfig(awsConfig)
	result, err := client.DescribeInstances(context.TODO(), &ec2.DescribeInstancesInput{
		Filters: []ec2Types.Filter{
			{
				Name:   aws.String("tag:Name"),
				Values: []string{cfg.InstanceName},
			},
			{
				Name:   aws.String("instance-state-name"),
				Values: []string{"running"},
			},
		},
	})
	if err != nil {
		return err
	}

	for _, reservation := range result.Reservations {
		for _, instance := range reservation.Instances {
			cfg.InstanceID = *instance.InstanceId
			cfg.InstanceAZ = *instance.Placement.AvailabilityZone
			cfg.Region = cfg.InstanceAZ[:len(cfg.InstanceAZ)-1]
			return nil
		}
	}
	return fmt.Errorf("instance not found or not in running state")
}

func sendSSHPublicKey() error {
	// send SSH public key
	client := ec2instanceconnect.NewFromConfig(awsConfig)

	publicKeyPath := fmt.Sprintf("%s/.ssh/id_rsa.pub", os.Getenv("HOME"))
	publicKey, err := os.ReadFile(publicKeyPath)
	if err != nil {
		return fmt.Errorf("failed to read SSH public key: %v", err)
	}

	_, err = client.SendSSHPublicKey(context.TODO(), &ec2instanceconnect.SendSSHPublicKeyInput{
		InstanceId:       aws.String(cfg.InstanceID),
		InstanceOSUser:   aws.String(cfg.InstanceUser),
		SSHPublicKey:     aws.String(string(publicKey)),
		AvailabilityZone: aws.String(cfg.InstanceAZ),
	})
	if err != nil {
		return fmt.Errorf("failed to send SSH public key: %v", err)
	}

	return nil
}

type StartSessionRequestData struct {
	Target       string              `json:"Target"`
	DocumentName string              `json:"DocumentName"`
	Parameters   map[string][]string `json:"Parameters"`
}

type StartSessionResponseData struct {
	SessionID  string `json:"SessionId"`
	StreamURL  string `json:"StreamUrl"`
	TokenValue string `json:"TokenValue"`
}

func startSSMSessionWithPlugin() error {
	ssmClient := ssm.NewFromConfig(awsConfig)

	// Use the custom struct for the request
	startSessionRequestData := StartSessionRequestData{
		Target:       cfg.InstanceID,
		DocumentName: "AWS-StartSSHSession",
		Parameters:   map[string][]string{"portNumber": {"22"}},
	}

	// Create the StartSessionInput for the API call
	startSessionInput := &ssm.StartSessionInput{
		Target:       aws.String(startSessionRequestData.Target),
		DocumentName: aws.String(startSessionRequestData.DocumentName),
		Parameters:   startSessionRequestData.Parameters,
	}

	// Call the StartSession API
	startSessionOutput, err := ssmClient.StartSession(context.TODO(), startSessionInput)
	if err != nil {
		return fmt.Errorf("failed to start SSM session: %v", err)
	}

	// Use the custom struct for the response
	startSessionResponseData := StartSessionResponseData{
		SessionID:  aws.ToString(startSessionOutput.SessionId),
		StreamURL:  aws.ToString(startSessionOutput.StreamUrl),
		TokenValue: aws.ToString(startSessionOutput.TokenValue),
	}

	// Marshal the custom structs to JSON
	startSessionResponse, err := json.Marshal(startSessionResponseData)
	if err != nil {
		return fmt.Errorf("failed to marshal start session response: %v", err)
	}

	startSessionRequest, err := json.Marshal(startSessionRequestData)
	if err != nil {
		return fmt.Errorf("failed to marshal start session request: %v", err)
	}

	endpoint := fmt.Sprintf("https://ssm.%s.amazonaws.com", cfg.Region)

	// find the session-manager-plugin binary using common paths
	var pluginPath string
	commonPaths := []string{
		"session-manager-plugin",                   // $PATH
		"/usr/local/bin/session-manager-plugin",    // default
		"/usr/bin/session-manager-plugin",          // linux
		"/opt/homebrew/bin/session-manager-plugin", // macos (homebrew)
	}
	for _, path := range commonPaths {
		if _, err := os.Stat(path); err == nil {
			pluginPath = path
			break
		}
	}
	if pluginPath == "" {
		return fmt.Errorf("session-manager-plugin binary not found")
	}

	// Correct the argument order based on the ValidateInputAndStartSession function
	// (see https://github.com/aws/session-manager-plugin/blob/mainline/src/sessionmanagerplugin/session/session.go)
	cmd := exec.Command(
		pluginPath,
		string(startSessionResponse), // args[1]: Session response
		cfg.Region,                   // args[2]: Client region
		"StartSession",               // args[3]: Operation name
		cfg.AwsProfile,               // args[4]: Profile name
		string(startSessionRequest),  // args[5]: Parameters input to AWS CLI for StartSession API
		endpoint,                     // args[6]: Endpoint for SSM service
	)

	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	slog.Info("session-manager-plugin start")
	if err := cmd.Run(); err != nil {
		slog.Error("session-manager-plugin error: %v", "error", err)
	}
	slog.Info("session-manager-plugin end")

	return nil
}
