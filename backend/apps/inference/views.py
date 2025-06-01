from django.shortcuts import render
from rest_framework import viewsets, status
from rest_framework.response import Response
from .models import InferenceRequest
from .serializers import InferenceRequestSerializer
from .tasks import process_inference_request
from rest_framework.decorators import api_view
from django.shortcuts import get_object_or_404
from apps.services.models import LLMModel
from langchain_openai import ChatOpenAI, OpenAI
from langchain.callbacks.manager import CallbackManager
from langchain.callbacks.streaming_stdout import StreamingStdOutCallbackHandler
import logging
from django.http import Http404, StreamingHttpResponse
import requests
import json

logger = logging.getLogger(__name__)

# Create your views here.


class InferenceRequestViewSet(viewsets.ModelViewSet):
    queryset = InferenceRequest.objects.all()
    serializer_class = InferenceRequestSerializer

    def get_queryset(self):
        queryset = super().get_queryset()
        tts_service_id = self.request.query_params.get('tts_service')
        image_gen_service_id = self.request.query_params.get('image_gen_service')
        if tts_service_id:
            queryset = queryset.filter(tts_service=tts_service_id)
        if image_gen_service_id:
            queryset = queryset.filter(image_gen_service=image_gen_service_id)
        return queryset

    def create(self, request, *args, **kwargs):
        serializer = self.get_serializer(data=request.data)
        serializer.is_valid(raise_exception=True)
        inference_request = serializer.save()

        # Enqueue the Celery task
        process_inference_request.delay(inference_request.id)

        return Response(
            {"request_id": inference_request.id}, status=status.HTTP_201_CREATED
        )

@api_view(['POST'])
def llm_inference(request):
    """
    Handle LLM inference requests in OpenAI API format.
    Supports both chat completions and text completions, including streaming.
    """
    logger.info("🚀 Starting LLM inference request")
    logger.debug(f"📦 Request data: {request.data}")

    # Get the LLM service
    try:
        model_name = request.data.get('model')
        if model_name:
            llm_service = get_object_or_404(LLMModel, name=model_name, is_active=True)
        else:
            # If no model specified, get the first active model
            llm_service = LLMModel.objects.filter(is_active=True).first()
            if not llm_service:
                raise Http404("No active LLM models found")
        logger.info(f"🔍 Found active LLM service: {llm_service.name} at {llm_service.base_url}")
    except Exception as e:
        logger.error(f"❌ Failed to get LLM service: {str(e)}")
        raise

    # Create inference request record
    try:
        inference_type = "llm_chat" if "messages" in request.data else "llm_completion"
        logger.info(f"📝 Creating inference request of type: {inference_type}")

        inference_request = InferenceRequest.objects.create(
            inference_type=inference_type,
            payload=request.data,
            llm_service=llm_service
        )
        logger.info(f"✅ Created inference request with ID: {inference_request.id}")
    except Exception as e:
        logger.error(f"❌ Failed to create inference request: {str(e)}")
        raise

    # Determine endpoint based on request body
    if "messages" in request.data:
        endpoint = llm_service.base_url.rstrip('/') + '/chat/completions'
    else:
        endpoint = llm_service.base_url.rstrip('/') + '/completions'

    headers = {
        "Authorization": f"Bearer dummy-key",
        "Content-Type": "application/json"
    }

    # Handle streaming
    stream = request.data.get("stream", False)
    try:
        logger.info(f"🌐 Sending request to LLM API at {endpoint} (stream={stream})")
        if stream:
            # Stream the response from the LLM API to the client and accumulate it
            api_response = requests.post(
                endpoint,
                json=request.data,
                headers=headers,
                stream=True,
                timeout=120
            )
            api_response.raise_for_status()
            streamed_chunks = []
            def stream_generator():
                try:
                    for chunk in api_response.iter_content(chunk_size=8192):
                        if chunk:
                            streamed_chunks.append(chunk)
                            yield chunk
                    # After streaming is done, save the accumulated response
                    full_data = b''.join(streamed_chunks).decode('utf-8')
                    lines = [line.strip() for line in full_data.split('\n') if line.strip().startswith('data: ')]
                    json_strs = [line.replace('data: ', '') for line in lines if line != 'data: [DONE]']
                    parsed = []
                    for js in json_strs:
                        try:
                            parsed.append(json.loads(js))
                        except Exception:
                            pass
                    inference_request.response = parsed
                    inference_request.status = "completed"
                    inference_request.save()
                    logger.info(f"✅ Saved streamed response for inference request {inference_request.id}")
                except Exception as e:
                    logger.error(f"❌ Failed to save streamed response: {str(e)}")
            response = StreamingHttpResponse(
                stream_generator(),
                content_type=api_response.headers.get('Content-Type', 'application/json')
            )
            response['Cache-Control'] = 'no-cache'
            response['X-Accel-Buffering'] = 'no'
            return response
        else:
            # Non-streaming: return the full JSON response
            api_response = requests.post(
                endpoint,
                json=request.data,
                headers=headers,
                timeout=60
            )
            api_response.raise_for_status()
            result = api_response.json()
    except requests.RequestException as e:
        logger.error(f"❌ LLM API request failed: {str(e)}")
        inference_request.status = "failed"
        inference_request.error_details = str(e)
        inference_request.save()
        return Response({"error": str(e)}, status=status.HTTP_502_BAD_GATEWAY)

    # Update inference request with response
    try:
        inference_request.response = result if not stream else None
        inference_request.status = "completed"
        inference_request.save()
        logger.info(f"✅ Updated inference request {inference_request.id} with response")
    except Exception as e:
        logger.error(f"❌ Failed to update inference request: {str(e)}")
        raise

    logger.info("🎉 Successfully completed LLM inference request")
    return Response(result)
