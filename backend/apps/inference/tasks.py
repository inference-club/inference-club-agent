import requests
import logging
import base64
from io import BytesIO
from PIL import Image
from django.core.files.base import ContentFile
from backend.celery_app import app
from .models import InferenceRequest

logger = logging.getLogger(__name__)

OPENAI_COMPATIBLE_API_URL = "http:/0.0.0.0:11434/v1"
NIM_FLUX_API_URL = "http://host.docker.internal:8001/v1"


def handle_image_generation(inference_request):
    """Handle image generation using the Flux API."""
    try:
        # Prepare the request to Flux API
        endpoint = f"{NIM_FLUX_API_URL}/infer"
        logger.info(
            f"Sending request to Flux API with payload: {inference_request.payload}"
        )

        response = requests.post(
            endpoint,
            json=inference_request.payload,
            headers={"Content-Type": "application/json"},
        )
        logger.info(
            f"Received response from Flux API with status code: {response.status_code}"
        )

        if response.status_code == 200:
            response_data = response.json()
            # Get the base64 image data from the response
            image_data = base64.b64decode(response_data["artifacts"][0]["base64"])

            # Create a PIL Image from the binary data
            image = Image.open(BytesIO(image_data))

            # Save the image to a BytesIO object
            image_io = BytesIO()
            image.save(image_io, format="PNG")
            image_io.seek(0)

            # Save the image to the model's generated_image field
            inference_request.generated_image.save(
                f"generated_image_{inference_request.id}.png",
                ContentFile(image_io.getvalue()),
                save=False,
            )

            # Store the full response in the response field
            inference_request.response = response_data
            inference_request.status = "completed"
            logger.info(
                f"Successfully processed image generation request {inference_request.id}"
            )
        else:
            inference_request.status = "failed"
            error_msg = f"Flux API request failed with status {response.status_code}: {response.text}"
            inference_request.error_details = error_msg
            logger.error(
                f"Failed to process image generation request {inference_request.id}: {error_msg}"
            )

        inference_request.save()
        return True

    except Exception as e:
        logger.error(
            f"Error in handle_image_generation for request {inference_request.id}: {str(e)}"
        )
        inference_request.status = "failed"
        inference_request.error_details = f"Image generation error: {str(e)}"
        inference_request.save()
        return False


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

        if inference_request.inference_type == "image_generation":
            handle_image_generation(inference_request)
            return

        # Handle LLM requests
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
