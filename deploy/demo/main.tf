# Always-on public demo: one small ARM instance running the same
# containers, with real SQS behind it. Costs about $10/month, versus
# roughly $60 for the autoscaling stack in ../terraform. The full
# architecture stays the reference design and is deployed on demand;
# this exists so the public link never goes dark.

terraform {
  required_version = ">= 1.9"
  required_providers {
    aws = {
      source  = "hashicorp/aws"
      version = "~> 6.0"
    }
  }
}

provider "aws" {
  region = var.region
  default_tags {
    tags = { Project = "blundernet", Stack = "demo" }
  }
}

variable "region" {
  type    = string
  default = "us-east-1"
}

variable "name" {
  type    = string
  default = "blundernet-demo"
}

variable "instance_type" {
  type    = string
  default = "t4g.micro"
}

# Engine strength. The box is burstable, so the demo runs fewer
# simulations than the Fargate worker to stay inside CPU credits.
variable "engine_sims" {
  type    = number
  default = 200
}

# Set to a domain you own (with an A record pointing at the instance's
# public IP) to switch the proxy from plain HTTP to automatic HTTPS.
variable "domain" {
  type    = string
  default = ""
}

data "aws_ami" "al2023" {
  most_recent = true
  owners      = ["amazon"]
  filter {
    name   = "name"
    values = ["al2023-ami-2023.*-arm64"]
  }
}

resource "aws_ecr_repository" "api" {
  name         = "${var.name}-api"
  force_delete = true
}

resource "aws_ecr_repository" "worker" {
  name         = "${var.name}-worker"
  force_delete = true
}

resource "aws_sqs_queue" "moves" {
  name                       = "${var.name}-moves"
  visibility_timeout_seconds = 30
  receive_wait_time_seconds  = 10
}

data "aws_iam_policy_document" "ec2_assume" {
  statement {
    actions = ["sts:AssumeRole"]
    principals {
      type        = "Service"
      identifiers = ["ec2.amazonaws.com"]
    }
  }
}

resource "aws_iam_role" "instance" {
  name               = "${var.name}-instance"
  assume_role_policy = data.aws_iam_policy_document.ec2_assume.json
}

# Pull images from ECR, drive the queue, and allow console access via
# Session Manager so the box needs no SSH key and no open port 22.
resource "aws_iam_role_policy_attachment" "ecr" {
  role       = aws_iam_role.instance.name
  policy_arn = "arn:aws:iam::aws:policy/AmazonEC2ContainerRegistryReadOnly"
}

resource "aws_iam_role_policy_attachment" "ssm" {
  role       = aws_iam_role.instance.name
  policy_arn = "arn:aws:iam::aws:policy/AmazonSSMManagedInstanceCore"
}

resource "aws_iam_role_policy" "sqs" {
  name = "sqs"
  role = aws_iam_role.instance.id
  policy = jsonencode({
    Version = "2012-10-17"
    Statement = [{
      Effect = "Allow"
      Action = [
        "sqs:SendMessage", "sqs:ReceiveMessage",
        "sqs:DeleteMessage", "sqs:GetQueueAttributes",
      ]
      Resource = aws_sqs_queue.moves.arn
    }]
  })
}

resource "aws_iam_instance_profile" "instance" {
  name = "${var.name}-instance"
  role = aws_iam_role.instance.name
}

resource "aws_security_group" "web" {
  name_prefix = "${var.name}-web-"
  description = "public web traffic"

  ingress {
    from_port   = 80
    to_port     = 80
    protocol    = "tcp"
    cidr_blocks = ["0.0.0.0/0"]
  }
  ingress {
    from_port   = 443
    to_port     = 443
    protocol    = "tcp"
    cidr_blocks = ["0.0.0.0/0"]
  }
  egress {
    from_port   = 0
    to_port     = 0
    protocol    = "-1"
    cidr_blocks = ["0.0.0.0/0"]
  }
}

resource "aws_instance" "demo" {
  ami                    = data.aws_ami.al2023.id
  instance_type          = var.instance_type
  iam_instance_profile   = aws_iam_instance_profile.instance.name
  vpc_security_group_ids = [aws_security_group.web.id]

  root_block_device {
    volume_size = 12
    volume_type = "gp3"
  }

  user_data_replace_on_change = true
  user_data = templatefile("${path.module}/user_data.sh.tftpl", {
    region      = var.region
    registry    = split("/", aws_ecr_repository.api.repository_url)[0]
    api_image   = "${aws_ecr_repository.api.repository_url}:latest"
    worker_ig   = "${aws_ecr_repository.worker.repository_url}:latest"
    queue_url   = aws_sqs_queue.moves.url
    engine_sims = var.engine_sims
    domain      = var.domain
  })
}

# Stable address so the public link survives instance replacement.
resource "aws_eip" "demo" {
  instance = aws_instance.demo.id
  domain   = "vpc"
}

output "url" {
  value = var.domain != "" ? "https://${var.domain}" : "http://${aws_eip.demo.public_ip}"
}

output "ip" {
  value = aws_eip.demo.public_ip
}

output "ecr_api" {
  value = aws_ecr_repository.api.repository_url
}

output "ecr_worker" {
  value = aws_ecr_repository.worker.repository_url
}

output "instance_id" {
  value = aws_instance.demo.id
}
