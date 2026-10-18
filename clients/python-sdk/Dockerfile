FROM python:3.14-slim
RUN pip install --no-cache-dir --disable-pip-version-check anthropic==1.15.0 \
 && { python --version; echo "anthropic $(pip show anthropic 2>/dev/null | awk '/^Version/{print $2}')"; echo "packages: $(pip list --format=freeze 2>/dev/null | grep -v -i '^pip=' | wc -l | tr -d ' ')"; } > /toolchain.txt
COPY client.py /app/client.py
ENTRYPOINT ["python", "/app/client.py"]
