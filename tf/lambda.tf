
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

variable "manifester_image_tag" {
  default = "DUMMY"
}

resource "aws_lambda_function" "manifester" {
  package_type = "Image"
  image_uri    = "${aws_ecr_repository.repo.repository_url}:${var.manifester_image_tag}"

  function_name = "styx-manifester"
  role          = aws_iam_role.iam_for_lambda.arn

  architectures = ["x86_64"] # TODO: can we make it run on arm?

  memory_size = 500 # MB
  ephemeral_storage {
    size = 512 # MB
  }
  timeout = 300 # seconds
  image_config {
    command = [
      "manifester",
      // must be in the same region:
      "--chunkbucket=${aws_s3_bucket.styx.id}",
      "--styx_ssm_signkey=${aws_ssm_parameter.manifester_signkey.name}",
    ]
  }
}

resource "aws_lambda_function_url" "manifester" {
  function_name      = aws_lambda_function.manifester.function_name
  authorization_type = "NONE"
  invoke_mode        = "RESPONSE_STREAM"
}
