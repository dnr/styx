
// iam for ec2:

data "aws_iam_policy_document" "assume_role_ec2" {
  statement {
    principals {
      type        = "Service"
      identifiers = ["ec2.amazonaws.com"]
    }
    actions = ["sts:AssumeRole"]
  }
}

data "aws_iam_policy_document" "charon_get_parameter" {
  statement {
    actions   = ["ssm:GetParameter"]
    resources = ["${aws_ssm_parameter.charon_signkey.arn}"]
  }
}

resource "aws_iam_role" "iam_for_charon" {
  name               = "iam_for_ec2_charon"
  assume_role_policy = data.aws_iam_policy_document.assume_role_ec2.json
  inline_policy {
    name   = "get-parameter"
    policy = data.aws_iam_policy_document.charon_get_parameter.json
  }
}

// parameter store

resource "aws_ssm_parameter" "charon_signkey" {
  name  = "styx-charon-signkey-test-1"
  type  = "SecureString"
  value = file("../keys/styx-nixcache-test-1.secret")
}

// security group

resource "aws_security_group" "worker_sg" {
  name        = "worker-sg"
  description = "Security group for workers"

  ingress {
    from_port   = 22
    to_port     = 22
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

// instance profile

resource "aws_iam_instance_profile" "charon_worker" {
  name  = "charon_worker_profile"
  roles = [aws_iam_role.iam_for_charon.name]
}

// ssh key

resource "aws_key_pair" "my_ssh_key" {
  public_key = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIHLw2kct3mhDYpJyrchof00gDxCVqgepql0OoRSNbdkY"
}

// asg

data "aws_ami" "nixos_amd64" {
  owners      = ["427812963091"]
  most_recent = true
  filter {
    name   = "name"
    values = ["nixos/24.05*"]
  }
  filter {
    name   = "architecture"
    values = ["amd64"]
  }
}

resource "aws_launch_template" "charon_worker" {
  name_prefix          = "charon-worker"
  image_id             = aws_ami.nixos_amd64.id
  instance_type        = "c7a.4xlarge"
  security_groups_ids  = [aws_security_group.worker_sg.id]
  iam_instance_profile = aws_iam_instance_profile.charon_instance_profile.name
  key_name             = aws_key_pair.my_ssh_key
  user_data            = filebase64("charon-worker-ud.nix")
  block_device_mappings {
    device_name = "/dev/xvda"
    ebs {
      volume_size = 40
    }
  }
  instance_market_options {
    market_type = "spot"
    spot_options {
      max_price = "0.40" # on-demand 0.8211
    }
  }
}

resource "aws_autoscaling_group" "charon_asg" {
  name = "charon-asg"

  min_size         = 0
  max_size         = 1
  desired_capacity = 0

  launch_template {
    id      = aws_launch_template.charon_worker.id
    version = "$Latest"
  }

  health_check_type = "EC2"
}
