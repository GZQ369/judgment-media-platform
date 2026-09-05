@echo off
setlocal
node "%~dp0bin\ego-browser.mjs" %*
exit /b %errorlevel%
