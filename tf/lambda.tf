
// iam for lambda:

data "aws_iam_policy_document" "assume_role_lambda" {
  statement {
    principals {
      type        = "Service"
      identifiers = ["lambda.amazonaws.com"]
    }
    actions = ["sts:AssumeRole"]
  }
}

data "aws_iam_policy_document" "manifester_get_parameter" {
  statement {
    actions   = ["ssm:GetParameter"]
    resources = ["${aws_ssm_parameter.manifester_signkey.arn}"]
  }
}

resource "aws_iam_role" "iam_for_lambda" {
  name                = "iam_for_lambda_styx"
  assume_role_policy  = data.aws_iam_policy_document.assume_role_lambda.json
  managed_policy_arns = ["arn:aws:iam::aws:policy/service-role/AWSLambdaBasicExecutionRole"]
  inline_policy {
    name   = "get-parameter"
    policy = data.aws_iam_policy_document.manifester_get_parameter.json
  }
}

// ecr:

resource "aws_ecr_repository" "repo" {
  name = "styx"
}

// s3:

resource "aws_s3_bucket" "styx" {
  bucket = "styx-1"
}

data "aws_iam_policy_document" "styx_bucket_policy" {
  statement {
    principals {
      type        = "*"
      identifiers = ["*"]
    }
    actions   = ["s3:GetObject"]
    resources = [aws_s3_bucket.styx.arn, "${aws_s3_bucket.styx.arn}/*"]
  }
  statement {
    principals {
      type = "AWS"
      identifiers = [
        aws_iam_role.iam_for_lambda.arn,
        aws_iam_role.iam_for_charon.arn,
      ]
    }
    actions   = ["s3:*"]
    resources = [aws_s3_bucket.styx.arn, "${aws_s3_bucket.styx.arn}/*"]
  }
}

resource "aws_s3_bucket_policy" "styx" {
  bucket = aws_s3_bucket.styx.id
  policy = data.aws_iam_policy_document.styx_bucket_policy.json
}

resource "aws_s3_bucket_lifecycle_configuration" "styx" {
  bucket = aws_s3_bucket.styx.id
  rule {
    id     = "nixcache-ttl"
    status = "Enabled"
    filter {
      prefix = "nixcache/"
    }
    abort_incomplete_multipart_upload { days_after_initiation = 1 }
    expiration { days = 365 }
  }
}

resource "aws_s3_bucket_public_access_block" "styx" {
  bucket                  = aws_s3_bucket.styx.id
  block_public_acls       = false
  block_public_policy     = false
  ignore_public_acls      = false
  restrict_public_buckets = false
}

resource "aws_s3_object" "styx-cache-info" {
  bucket  = aws_s3_bucket.styx.id
  key     = "nixcache/nix-cache-info"
  content = "StoreDir: /nix/store\nPriority: 90\n"
}

resource "aws_s3_object" "styx-params-test-1" {
  bucket = aws_s3_bucket.styx.id
  key    = "params/test-1"
  source = "../params/test-1.signed"
}

// parameter store

resource "aws_ssm_parameter" "manifester_signkey" {
  name  = "styx-manifester-signkey-test-1"
  type  = "SecureString"
  value = file("../keys/styx-test-1.secret")
}

// lambda:

variable "manifester_image_tag" {}

variable "lambda_memory_sizes" {
  type        = list(number)
  default     = [500, 1500] # MB
}

resource "aws_lambda_function" "manifester" {
  count = length(var.lambda_memory_sizes)

  package_type = "Image"
  image_uri    = "${aws_ecr_repository.repo.repository_url}:${var.manifester_image_tag}"

  function_name = count.index == 0 ? "styx-manifester" : "styx-manifester-${count.index}"
  role          = aws_iam_role.iam_for_lambda.arn

  architectures = ["x86_64"] # TODO: can we make it run on arm?

  memory_size = var.lambda_memory_sizes[count.index]
  ephemeral_storage {
    size = 512 # MB
  }
  timeout = 300 # seconds
  image_config {
    command = [
      "manifester",
      # must be in the same region:
      "--chunkbucket=${aws_s3_bucket.styx.id}",
      "--styx_ssm_signkey=${aws_ssm_parameter.manifester_signkey.name}",
      # Uncomment these to allow manifester to build from styx nix cache on-demand.
      # This shouldn't be necessary since CI pre-builds manifests and they should
      # be cached.
      # "--allowed_upstream=cache.nixos.org",
      # "--allowed_upstream=${aws_s3_bucket.styx.id}.s3.amazonaws.com",
      # "--nix_pubkey=cache.nixos.org-1:6NCHdD59X431o0gWypbMrAURkbJ16ZPMQFGspcDShjY=",
      # "--nix_pubkey=${trimspace(file("../keys/styx-nixcache-test-1.public"))}",
    ]
  }

  # logging and metrics with axiom
  environment {
    variables = {
      AXIOM_TOKEN   = trimspace(file("../keys/axiom-styx-lambda.secret"))
      AXIOM_DATASET = "styx"
    }
  }
}

resource "aws_lambda_function_url" "manifester" {
  count = length(var.lambda_memory_sizes)

  function_name      = aws_lambda_function.manifester[count.index].function_name
  authorization_type = "NONE"
  invoke_mode        = "RESPONSE_STREAM"
}
