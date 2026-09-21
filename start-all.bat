@echo off
title Aegis All-in-One Launcher
cd /d "%~dp0"

echo ================================================================
echo         Aegis: Starting 3-Node Cluster + Web Dashboard          
echo ================================================================

echo 1. Launching Aegis Raft Cluster Nodes (1, 2, 3)...
powershell -ExecutionPolicy Bypass -File "%~dp0start-cluster.ps1"

echo 2. Launching Next.js Web Dashboard on port 3000...
start "Aegis Dashboard" cmd /c "cd /d %~dp0dashboard\cluster_ui && npm run dev"

echo 3. Opening browser in 3 seconds...
timeout /t 3 /nobreak >nul
start http://localhost:3000

echo ================================================================
echo Aegis Cluster & Landing Page are RUNNING!
echo Close this window or run stop-cluster.ps1 to shut down.
echo ================================================================
pause
