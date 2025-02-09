
// iam for heavy worker on ec2:

data "aws_iam_policy_document" "assume_role_ec2" {
  statement {
    principals {
      type        = "Service"
      identifiers = ["ec2.amazonaws.com"]
    }
    actions = ["sts:AssumeRole"]
  }
}

data "aws_iam_policy_document" "charon_get_parameters" {
  statement {
    actions = ["ssm:GetParameter"]
    resources = [
      aws_ssm_parameter.charon_temporal_params.arn,
      aws_ssm_parameter.charon_signkey.arn,
      aws_ssm_parameter.manifester_signkey.arn,
    ]
  }
}

resource "aws_iam_role" "iam_for_charon" {
  name               = "iam_for_ec2_charon"
  assume_role_policy = data.aws_iam_policy_document.assume_role_ec2.json
  inline_policy {
    name   = "get-parameter"
    policy = data.aws_iam_policy_document.charon_get_parameters.json
  }
}

// iam for scaler only

data "aws_iam_policy_document" "charon_asg_scaler" {
  statement {
    actions = [
      "autoscaling:DescribeAutoScalingGroups",
      "autoscaling:SetDesiredCapacity",
    ]
    resources = [
      aws_autoscaling_group.charon_asg.arn,
    ]
  }
}

resource "aws_iam_user" "charon_asg_scaler" {
  name = "charon_asg_scaler"
}

resource "aws_iam_user_policy" "charon_asg_scaler" {
  user   = aws_iam_user.charon_asg_scaler.name
  policy = data.aws_iam_policy_document.charon_asg_scaler.json
}

resource "aws_iam_access_key" "charon_asg_scaler" {
  user = aws_iam_user.charon_asg_scaler.name
}

resource "local_sensitive_file" "charon_asg_scaler" {
  content         = <<-EOF
    [default]
    aws_access_key_id = ${aws_iam_access_key.charon_asg_scaler.id}
    aws_secret_access_key = ${aws_iam_access_key.charon_asg_scaler.secret}
  EOF
  filename        = "../keys/charon-asg-scaler-creds.secret"
  file_permission = 0600
}

// parameter store

resource "aws_ssm_parameter" "charon_signkey" {
  name  = "styx-charon-signkey-test-1"
  type  = "SecureString"
  value = trimspace(file("../keys/styx-nixcache-test-1.secret"))
}

resource "aws_ssm_parameter" "charon_temporal_params" {
  name  = "styx-charon-temporal-params"
  type  = "SecureString"
  value = trimspace(file("../keys/temporal-creds-charon.secret"))
}

// security group

resource "aws_security_group" "worker_sg" {
  name        = "charon-worker-sg"
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
  name = "charon_worker_profile"
  role = aws_iam_role.iam_for_charon.name
}

// ssh key

resource "aws_key_pair" "my_ssh_key" {
  public_key = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIHLw2kct3mhDYpJyrchof00gDxCVqgepql0OoRSNbdkY"
}

// asg

data "aws_ami" "nixos_x86_64" {
  owners      = ["427812963091"]
  most_recent = true
  filter {
    name   = "name"
    values = ["nixos/24.11*"]
  }
  filter {
    name   = "architecture"
    values = ["x86_64"]
  }
}

variable "charon_storepath" {}

resource "aws_launch_template" "charon_worker" {
  name_prefix          = "charon-worker"
  image_id             = data.aws_ami.nixos_x86_64.id
  security_group_names = [aws_security_group.worker_sg.name]
  iam_instance_profile {
    arn = aws_iam_instance_profile.charon_worker.arn
  }
  key_name = aws_key_pair.my_ssh_key.id
  user_data = base64encode(templatefile("charon-worker-ud.nix", {
    sub           = "https://${aws_s3_bucket.styx.id}.s3.amazonaws.com/nixcache/"
    pubkey        = trimspace(file("../keys/styx-nixcache-test-1.public"))
    charon        = var.charon_storepath
    tmpssm        = aws_ssm_parameter.charon_temporal_params.name
    cachessm      = aws_ssm_parameter.charon_signkey.id
    bucket        = aws_s3_bucket.styx.id
    styxssm       = aws_ssm_parameter.manifester_signkey.name
    nixoskey      = "cache.nixos.org-1:6NCHdD59X431o0gWypbMrAURkbJ16ZPMQFGspcDShjY="
    axiom_dataset = "styx"
    axiom_token   = trimspace(file("../keys/axiom-styx-charon-heavy.secret"))
  }))
  block_device_mappings {
    device_name = "/dev/xvda"
    ebs {
      volume_size = 60
      volume_type = "gp3"
    }
  }
  instance_type = "c7a.4xlarge"
  instance_market_options {
    market_type = "spot"
    spot_options {
      max_price = "0.40" # c7a.4xlarge on-demand: 0.8211
    }
  }
}

resource "aws_autoscaling_group" "charon_asg" {
  name = "charon-asg"

  min_size = 0
  max_size = 1
  #desired_capacity = 0

  # Note: 1e apparently has no c7a.4xlarge? that alias is account-specific, another account
  # will have to use a different set here.
  availability_zones = ["us-east-1a", "us-east-1b", "us-east-1c", "us-east-1d", "us-east-1f"]

  launch_template {
    id      = aws_launch_template.charon_worker.id
    version = "$Latest"
  }

  health_check_type = "EC2"
}
