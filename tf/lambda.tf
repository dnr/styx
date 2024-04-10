
// iam for lambda:

data "aws_iam_policy_document" "assume_role" {
  statement {
    principals {
      type        = "Service"
      identifiers = ["lambda.amazonaws.com"]
    }
    actions = ["sts:AssumeRole"]
  }
}

resource "aws_iam_role" "iam_for_lambda" {
  name                = "iam_for_lambda_styx"
  assume_role_policy  = data.aws_iam_policy_document.assume_role.json
  managed_policy_arns = ["arn:aws:iam::aws:policy/service-role/AWSLambdaBasicExecutionRole"]
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
      type        = "AWS"
      identifiers = [aws_iam_role.iam_for_lambda.arn]
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

// lambda:

variable "manifester_image_tag" {}

resource "aws_lambda_function" "manifester" {
  package_type = "Image"
  image_uri    = "${aws_ecr_repository.repo.repository_url}:${var.manifester_image_tag}"

  function_name = "nix-sandwich-manifester"
  role          = aws_iam_role.iam_for_lambda.arn

  architectures = ["x86_64"] # TODO: can we make it run on arm?

  memory_size = 500 # MB
  ephemeral_storage {
    size = 500 # MB
  }
  timeout = 300 # seconds
  image_config {
    command = [
      "manifester"
      // must be in the same region:
      "--chunkbucket=${aws_s3_bucket.cache.id}"
      # TODO: use ssm parameter store or secrets manager or kms
      "--styx_signkey=/signkey"
    ]
  }
}

resource "aws_lambda_function_url" "manifester" {
  function_name      = aws_lambda_function.manifester.function_name
  authorization_type = "NONE"
  invoke_mode        = "RESPONSE_STREAM"
}
