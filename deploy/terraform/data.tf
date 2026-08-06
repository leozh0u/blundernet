# Stateful pieces: Redis (live game state + pub/sub), Postgres (archive),
# SQS (move-evaluation jobs).

resource "aws_elasticache_subnet_group" "redis" {
  name       = "${var.name}-redis"
  subnet_ids = aws_subnet.public[*].id
}

resource "aws_elasticache_cluster" "redis" {
  cluster_id           = "${var.name}-redis"
  engine               = "redis"
  node_type            = "cache.t4g.micro"
  num_cache_nodes      = 1
  parameter_group_name = "default.redis7"
  subnet_group_name    = aws_elasticache_subnet_group.redis.name
  security_group_ids   = [aws_security_group.redis.id]
}

resource "aws_db_subnet_group" "db" {
  name       = "${var.name}-db"
  subnet_ids = aws_subnet.public[*].id
}

resource "aws_db_instance" "db" {
  identifier             = "${var.name}-db"
  engine                 = "postgres"
  engine_version         = "17"
  instance_class         = "db.t4g.micro"
  allocated_storage      = 20
  db_name                = "blundernet"
  username               = "blundernet"
  password               = var.db_password
  db_subnet_group_name   = aws_db_subnet_group.db.name
  vpc_security_group_ids = [aws_security_group.db.id]
  skip_final_snapshot    = true
}

resource "aws_sqs_queue" "moves_dlq" {
  name                      = "${var.name}-moves-dlq"
  message_retention_seconds = 1209600
}

resource "aws_sqs_queue" "moves" {
  name                       = "${var.name}-moves"
  visibility_timeout_seconds = 30
  receive_wait_time_seconds  = 10
  redrive_policy = jsonencode({
    deadLetterTargetArn = aws_sqs_queue.moves_dlq.arn
    maxReceiveCount     = 5
  })
}

resource "aws_ecr_repository" "api" {
  name         = "${var.name}-api"
  force_delete = true
}

resource "aws_ecr_repository" "worker" {
  name         = "${var.name}-worker"
  force_delete = true
}
