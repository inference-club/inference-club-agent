import requests
import logging
from backend.celery_app import app
from .models import InferenceRequest

logger = logging.getLogger(__name__)

OPENAI_COMPATIBLE_API_URL = "http://host.docker.internal:11434/v1"


@app.task
def process_inference_request(request_id):
    try:
        # Get the inference request
        inference_request = InferenceRequest.objects.get(id=request_id)
        logger.info(f"Processing inference request {request_id}")

        # Update status to in_progress
        inference_request.status = "in_progress"
        inference_request.save()
        logger.info(f"Updated status to in_progress for request {request_id}")

        # Determine the endpoint based on inference type
        endpoint = (
            f"{OPENAI_COMPATIBLE_API_URL}/chat/completions"
            if inference_request.inference_type == "llm_chat"
            else f"{OPENAI_COMPATIBLE_API_URL}/completions"
        )
        logger.info(f"Using endpoint: {endpoint}")

        # Make the API request
        logger.info(
            f"Sending request to Ollama with payload: {inference_request.payload}"
        )
        response = requests.post(
            endpoint,
            json=inference_request.payload,
            headers={"Content-Type": "application/json"},
        )
        logger.info(
            f"Received response from Ollama with status code: {response.status_code}"
        )

        # Handle the response
        if response.status_code == 200:
            inference_request.response = response.json()
            inference_request.status = "completed"
            logger.info(f"Successfully processed request {request_id}")
        else:
            inference_request.status = "failed"
            error_msg = f"API request failed with status {response.status_code}: {response.text}"
            inference_request.error_details = error_msg
            logger.error(f"Failed to process request {request_id}: {error_msg}")

        inference_request.save()

    except InferenceRequest.DoesNotExist:
        logger.error(f"Inference request {request_id} not found")
    except requests.exceptions.RequestException as e:
        logger.error(f"Request error for inference request {request_id}: {str(e)}")
        if inference_request:
            inference_request.status = "failed"
            inference_request.error_details = f"Request error: {str(e)}"
            inference_request.save()
    except Exception as e:
        logger.error(f"Unexpected error processing request {request_id}: {str(e)}")
        if inference_request:
            inference_request.status = "failed"
            inference_request.error_details = f"Unexpected error: {str(e)}"
            inference_request.save()
