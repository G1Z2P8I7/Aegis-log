@echo off
title KhorosLog Web Visualizer (Port 3000)
cd /d "%~dp0dashboard\cluster_ui"

echo ================================================================
echo       KhorosLog: Starting Live Cluster UI on Port 3000          
echo ================================================================

start http://localhost:3000
npm run dev
