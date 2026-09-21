@echo off
title Aegis 3-Node Cluster Launcher
cd /d "%~dp0"

echo ================================================================
echo        Aegis: Launching 3-Node Distributed Cluster               
echo ================================================================

powershell -ExecutionPolicy Bypass -File "%~dp0start-cluster.ps1"

pause
