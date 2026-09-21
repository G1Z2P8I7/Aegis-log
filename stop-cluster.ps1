# KhorosLog Cluster Shutdown Script
Write-Host "Shutting down KhorosLog 3-node cluster..." -ForegroundColor Yellow

$procs = Get-NetTCPConnection -LocalPort 10001, 10002, 10003, 8001, 8002, 8003, 9001, 9002, 9003 -ErrorAction SilentlyContinue | 
    Select-Object -ExpandProperty OwningProcess -Unique

if ($procs) {
    foreach ($procId in $procs) {
        Stop-Process -Id $procId -Force -ErrorAction SilentlyContinue
    }
    Write-Host "All KhorosLog cluster processes terminated." -ForegroundColor Green
} else {
    Write-Host "No active KhorosLog cluster processes found." -ForegroundColor Gray
}
