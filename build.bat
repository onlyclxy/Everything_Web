@echo off
echo ���ڱ��������Ż��������...
echo.
echo ?? ��������:
echo - ������������: ��ҳ˲����Ӧ  
echo - ��ʽ�����Լ��: �Զ�ʶ����Ƶ��ʽ
echo - AVI/MKV�ȸ�ʽ: ������ʾ�ͱ��÷���
echo - ������־��¼: ��ϸ������״̬
echo.

go build -o everything-web-server-final.exe main.go
if %errorlevel% equ 0 (
    echo ? ����ɹ�! ���� everything-web-server-final.exe
    echo.
    echo ?? ���Խ���:
    echo 1. ���� ext:png - ���Դ����������
    echo 2. ���� ext:mp4 - ���Լ��ݸ�ʽ����  
    echo 3. ���� ext:avi - ���Ը�ʽ��������ʾ
    echo 4. ���� /api/cache-status - �鿴����״̬
    echo.
    echo ?? ���ڿ�������: everything-web-server-final.exe
) else (
    pause
) 