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
from django.http import Http404

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
            queryset = queryset.filter(tts_service_id=tts_service_id)
        if image_gen_service_id:
            queryset = queryset.filter(image_gen_service_id=image_gen_service_id)
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
    Supports both chat completions and text completions.
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

    try:
        # Configure the LLM client
        if "messages" in request.data:
            logger.info("💬 Processing chat completion request")
            # Chat completion
            try:
                # Prepare LLM parameters
                llm_params = {
                    "base_url": llm_service.base_url,
                    "api_key": "dummy-key",  # Using dummy key as specified
                    "model": request.data.get("model", "Qwen/Qwen3-8B"),
                    "temperature": request.data.get("temperature", 0.7),
                }
                # Only add max_tokens if it's provided and is a valid number
                max_tokens = request.data.get("max_tokens")
                if max_tokens is not None and isinstance(max_tokens, (int, float)):
                    llm_params["max_tokens"] = int(max_tokens)

                logger.debug(f"🤖 Configuring ChatOpenAI with params: {llm_params}")
                llm = ChatOpenAI(**llm_params)

                logger.info("🔄 Invoking LLM with messages")
                response = llm.invoke(request.data["messages"])
                logger.info("✅ Received response from LLM")

                result = {
                    "id": f"chatcmpl-{inference_request.id}",
                    "object": "chat.completion",
                    "created": inference_request.created_at.timestamp(),
                    "model": request.data.get("model", "Qwen/Qwen3-8B"),
                    "choices": [{
                        "index": 0,
                        "message": {
                            "role": "assistant",
                            "content": response.content
                        },
                        "finish_reason": "stop"
                    }],
                    "usage": {
                        "prompt_tokens": 0,  # TODO: implement token counting
                        "completion_tokens": 0,
                        "total_tokens": 0
                    }
                }
                logger.debug(f"📤 Prepared chat completion response: {result}")
            except Exception as e:
                logger.error(f"❌ Chat completion failed: {str(e)}")
                raise
        else:
            logger.info("📝 Processing text completion request")
            # Text completion
            try:
                # Prepare LLM parameters
                llm_params = {
                    "base_url": llm_service.base_url,
                    "api_key": "dummy-key",  # Using dummy key as specified
                    "model": request.data.get("model", "Qwen/Qwen3-8B"),
                    "temperature": request.data.get("temperature", 0.7),
                }
                # Only add max_tokens if it's provided and is a valid number
                max_tokens = request.data.get("max_tokens")
                if max_tokens is not None and isinstance(max_tokens, (int, float)):
                    llm_params["max_tokens"] = int(max_tokens)

                logger.debug(f"🤖 Configuring OpenAI with params: {llm_params}")
                llm = OpenAI(**llm_params)

                logger.info("🔄 Invoking LLM with prompt")
                response = llm.invoke(request.data["prompt"])
                logger.info("✅ Received response from LLM")

                result = {
                    "id": f"cmpl-{inference_request.id}",
                    "object": "text_completion",
                    "created": inference_request.created_at.timestamp(),
                    "model": request.data.get("model", "Qwen/Qwen3-8B"),
                    "choices": [{
                        "text": response,
                        "index": 0,
                        "finish_reason": "stop"
                    }],
                    "usage": {
                        "prompt_tokens": 0,  # TODO: implement token counting
                        "completion_tokens": 0,
                        "total_tokens": 0
                    }
                }
                logger.debug(f"📤 Prepared text completion response: {result}")
            except Exception as e:
                logger.error(f"❌ Text completion failed: {str(e)}")
                raise

        # Update inference request with response
        try:
            inference_request.response = result
            inference_request.status = "completed"
            inference_request.save()
            logger.info(f"✅ Updated inference request {inference_request.id} with response")
        except Exception as e:
            logger.error(f"❌ Failed to update inference request: {str(e)}")
            raise

        logger.info("🎉 Successfully completed LLM inference request")
        return Response(result)

    except Exception as e:
        logger.error(f"❌ LLM inference failed: {str(e)}")
        inference_request.status = "failed"
        inference_request.error_details = str(e)
        inference_request.save()
        logger.info(f"📝 Updated inference request {inference_request.id} with error details")
        return Response(
            {"error": str(e)},
            status=status.HTTP_500_INTERNAL_SERVER_ERROR
        )
