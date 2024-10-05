# ssm-ssh-connect

Gain the convenience of AWS Session Manager's keyless access with the flexibility of traditional SSH configurations.

## Why use ssm-ssh-connect?

AWS offers a service called Session Manager, which lets you [connect to EC2 instances](https://docs.aws.amazon.com/cli/latest/reference/ssm/start-session.html) via WebSocket connection (good for security, since you don't have to open SSH port to public).
However, while the AWS CLI lets you start a session with an instance via Session Manager, it lacks some of the key features that make SSH connections so powerful.
For instance, many tools such as database clients and Docker contexts support SSH, but not `aws ssm`. Additionally, SSH config allows you to use powerful features like user definitions, aliases, and wildcards.

`ssm-ssh-connect` bridges this gap by allowing you to use AWS Session Manager as if you were using traditional SSH, without losing any functionality. You can still use your SSH configurations but no longer need to manage SSH keys.

## How It Works

The script:
- automatically retrieves the instance ID using the EC2 instance name (and caches it for future use to speed up subsequent connections)
- pushes your public key to the instance (if it's not already there)
- uses the `session-manager-plugin` directly to establish the session

### Usage (ssh config examples):

Single Instance:

```
Host ec2-instance-name
User ubuntu
ProxyCommand ~/path/to/ssm-ssh-connect <aws-profile-name> %h %r
```

Multiple Instances (wildcard):

```
Host prd-* qa-* dev-*
User ubuntu
ProxyCommand ~/path/to/ssm-ssh-connect <aws-profile-name> %h %r
```

## Prerequisites

Before you start, make sure you have:
- AWS CLI installed and configured with the appropriate access.
- `session-manager-plugin` [installed](https://docs.aws.amazon.com/systems-manager/latest/userguide/session-manager-working-with-install-plugin.html).

## Installation

At the moment, the script is not available as a package. You have to build it from the source code (simple `go build` or `go install`).
