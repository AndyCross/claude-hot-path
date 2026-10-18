FROM python:3.14-slim
RUN python --version > /toolchain.txt
COPY client.py /app/client.py
ENTRYPOINT ["python", "/app/client.py"]
