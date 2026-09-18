package main

type taskTemplate struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
	Command     string `json:"command"`
	Type        string `json:"type"`
	IntervalMin int    `json:"interval_min,omitempty"`
	CronExpr    string `json:"cron_expr,omitempty"`
	Category    string `json:"category"`
	TimeoutSec  int    `json:"timeout_sec"`
}

var builtinTemplates = []taskTemplate{
	{
		ID: "backup-appdata", Name: "AppData Backup",
		Description: "Archive /DATA/AppData to /DATA/backups/",
		Command:     "mkdir -p /DATA/backups && tar -czf /DATA/backups/appdata_$(date +%Y%m%d_%H%M%S).tar.gz -C /DATA AppData",
		Type:        "cron", CronExpr: "0 2 * * *",
		Category: "backup", TimeoutSec: 600,
	},
	{
		ID: "cleanup-tmp", Name: "Cleanup Temp Files",
		Description: "Remove files older than 7 days from /tmp",
		Command:     "find /tmp -type f -mtime +7 -delete 2>/dev/null; echo cleaned",
		Type:        "cron", CronExpr: "0 4 * * 0",
		Category: "maintenance", TimeoutSec: 120,
	},
	{
		ID: "health-check", Name: "System Health Check",
		Description: "Check disk space, memory, and load average",
		Command:     "echo '=== Disk ===' && df -h / /DATA 2>/dev/null && echo '=== Memory ===' && free -h && echo '=== Load ===' && uptime",
		Type:        "interval", IntervalMin: 30,
		Category: "monitoring", TimeoutSec: 30,
	},
	{
		ID: "docker-prune", Name: "Docker Cleanup",
		Description: "Remove unused Docker images, containers and networks (volumes are kept)",
		// --volumes is deliberately absent: it would delete the data volume of
		// every stopped app, which on a NAS is exactly the data worth keeping.
		Command: "DOCKER_CONFIG=/DATA/.docker docker system prune -af 2>&1 || echo 'docker not available'",
		Type:    "cron", CronExpr: "0 3 * * 0",
		Category: "maintenance", TimeoutSec: 300,
	},
	{
		ID: "update-check", Name: "System Update Check",
		Description: "Check for available system updates",
		Command:     "cat /etc/os-release && echo '---' && uname -r",
		Type:        "cron", CronExpr: "0 8 * * 1",
		Category: "monitoring", TimeoutSec: 60,
	},
	{
		ID: "ssl-cert-check", Name: "SSL Certificate Expiry Check",
		Description: "Check SSL certificate expiry for a domain",
		Command:     "echo | openssl s_client -connect example.com:443 -servername example.com 2>/dev/null | openssl x509 -noout -dates 2>/dev/null || echo 'openssl not available'",
		Type:        "cron", CronExpr: "0 9 * * *",
		Category: "monitoring", TimeoutSec: 30,
	},
	{
		ID: "docker-status", Name: "Docker Container Status",
		Description: "List all Docker containers with status and resource usage",
		Command:     "docker ps -a --format 'table {{.Names}}\t{{.Status}}\t{{.Ports}}' 2>&1 && echo '---' && docker stats --no-stream --format 'table {{.Name}}\t{{.CPUPerc}}\t{{.MemUsage}}' 2>&1 || echo 'docker not available'",
		Type:        "interval", IntervalMin: 15,
		Category: "monitoring", TimeoutSec: 30,
	},
}
