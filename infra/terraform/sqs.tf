# Harvest extraction queue (daemon uploads raw turns → worker OpenRouter).
resource "aws_sqs_queue" "harvest" {
  name                       = "${var.project}-harvest"
  visibility_timeout_seconds = 180
  message_retention_seconds  = 1209600 # 14d
  receive_wait_time_seconds  = 10
}

resource "aws_iam_role_policy" "task_sqs_harvest" {
  name = "${var.project}-sqs-harvest"
  role = aws_iam_role.task.id
  policy = jsonencode({
    Version = "2012-10-17"
    Statement = [{
      Effect = "Allow"
      Action = [
        "sqs:SendMessage",
        "sqs:ReceiveMessage",
        "sqs:DeleteMessage",
        "sqs:GetQueueAttributes",
      ]
      Resource = [aws_sqs_queue.harvest.arn]
    }]
  })
}
