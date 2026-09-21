# Aegis 3-Node Cluster Launcher
[CmdletBinding()]
param()

$scriptDir = Split-Path -Parent $MyInvocation.MyCommand.Path
if (-not $scriptDir) { $scriptDir = (Get-Location).Path }
Set-Location $scriptDir

Write-Host "================================================================" -ForegroundColor Cyan
Write-Host "       Aegis: Launching 3-Node Distributed Cluster              " -ForegroundColor Green
Write-Host "================================================================" -ForegroundColor Cyan

$binPath = Join-Path $scriptDir "bin\aegis-node.exe"

# 1. Compile latest binary
Write-Host "Compiling latest aegis-node.exe..." -ForegroundColor Yellow
$env:Path = "D:\Installs\GO\bin;D:\Installs\MinGW\bin;" + $env:Path
go build -o "$binPath" ./cmd/node/main.go
if (-not (Test-Path $binPath)) {
    Write-Host "Error: Failed to build aegis-node.exe" -ForegroundColor Red
    exit 1
}

# 2. Ensure data directories exist
New-Item -ItemType Directory -Path "$scriptDir\data\node-1", "$scriptDir\data\node-2", "$scriptDir\data\node-3" -Force | Out-Null

# 3. Clean up any previous cluster processes
Get-Process aegis-node, khoroslog-node -ErrorAction SilentlyContinue | Stop-Process -Force -ErrorAction SilentlyContinue
Start-Sleep -Milliseconds 300

# 4. Launch Node 1, 2, 3 in visible windows so logs are viewable in real time
$nodes = @(
    @{ ID="1"; Port="8001"; Peer="9001"; Http="10001"; Peers="127.0.0.1:9002,127.0.0.1:9003" },
    @{ ID="2"; Port="8002"; Peer="9002"; Http="10002"; Peers="127.0.0.1:9001,127.0.0.1:9003" },
    @{ ID="3"; Port="8003"; Peer="9003"; Http="10003"; Peers="127.0.0.1:9001,127.0.0.1:9002" }
)

foreach ($n in $nodes) {
    Write-Host "Starting Node $($n.ID) (Client: $($n.Port), Peer: $($n.Peer), HTTP: $($n.Http))..." -ForegroundColor Yellow
    $dataDir = Join-Path $scriptDir "data\node-$($n.ID)"
    $argsList = @(
        "--id=$($n.ID)",
        "--port=$($n.Port)",
        "--peer-port=$($n.Peer)",
        "--http-port=$($n.Http)",
        "--peers=$($n.Peers)",
        "--data-dir=$dataDir"
    )

    Start-Process -FilePath $binPath -ArgumentList $argsList -WorkingDirectory $scriptDir
}

Write-Host "`nWaiting for cluster nodes to establish Raft quorum..." -ForegroundColor Cyan

# 5. Wait for nodes to report status
$leaderElected = $false
$leaderId = ""
$term = 0

for ($i = 0; $i -lt 15; $i++) {
    Start-Sleep -Milliseconds 400
    try {
        foreach ($port in @(10001, 10002, 10003)) {
            $status = Invoke-RestMethod -Uri "http://127.0.0.1:$port/api/status" -TimeoutSec 1 -ErrorAction SilentlyContinue
            if ($status -and $status.role -eq "LEADER") {
                $leaderElected = $true
                $leaderId = $status.id
                $term = $status.term
                break
            }
        }
        if ($leaderElected) { break }
    } catch {}
}

Write-Host "`n================================================================" -ForegroundColor Cyan
if ($leaderElected) {
    Write-Host " SUCCESS: 3-Node Cluster is ONLINE with Raft Quorum!" -ForegroundColor Green
    Write-Host "   Active Leader : Node $leaderId" -ForegroundColor White
    Write-Host "   Consensus Term: $term" -ForegroundColor White
} else {
    Write-Host " Cluster is ONLINE (Leader election in progress)" -ForegroundColor Yellow
}
Write-Host "================================================================" -ForegroundColor Cyan
Write-Host " Node Endpoints:" -ForegroundColor Gray
Write-Host "   • Node 1: http://127.0.0.1:10001 (Client TCP: 8001)" -ForegroundColor Gray
Write-Host "   • Node 2: http://127.0.0.1:10002 (Client TCP: 8002)" -ForegroundColor Gray
Write-Host "   • Node 3: http://127.0.0.1:10003 (Client TCP: 8003)" -ForegroundColor Gray

Write-Host "`nTo view the live animated cluster topology and chaos panel:" -ForegroundColor Cyan
Write-Host "   cd dashboard\cluster_ui" -ForegroundColor White
Write-Host "   npm run dev" -ForegroundColor White
Write-Host "   Open http://localhost:3000 in your browser" -ForegroundColor White
Write-Host "`n(To stop all nodes at any time, run .\stop-cluster.ps1)" -ForegroundColor DarkGray
