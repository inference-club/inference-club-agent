from django.shortcuts import render, get_object_or_404
from rest_framework import viewsets, status
from rest_framework.decorators import api_view, permission_classes
from rest_framework.permissions import AllowAny, IsAuthenticated
from rest_framework.response import Response
from .models import LLMModel, ImageGenModel, TTSService, VideoGenService
from .serializers import (
    LLMModelSerializer,
    ImageGenModelSerializer,
    TTSServiceSerializer,
    VideoGenServiceSerializer,
)
from apps.inference.models import InferenceRequest
from apps.inference.serializers import InferenceRequestSerializer
from apps.inference.tasks import process_image_gen_inference_request, process_tts_inference_request, process_video_gen_inference_request
from django.core.files.uploadedfile import InMemoryUploadedFile, TemporaryUploadedFile
import logging

logger = logging.getLogger(__name__)

# Create your views here.

class LLMModelViewSet(viewsets.ModelViewSet):
    """
    API endpoint for managing LLM models.
    """
    queryset = LLMModel.objects.all()
    serializer_class = LLMModelSerializer
    lookup_field = 'slug'

    def perform_create(self, serializer):
        logger.info("📝 Creating LLM model: %s", serializer.validated_data.get('name'))
        serializer.save()

    def perform_update(self, serializer):
        logger.info("✏️ Updating LLM model: %s", serializer.validated_data.get('name'))
        serializer.save()


class ImageGenModelViewSet(viewsets.ModelViewSet):
    """
    API endpoint for managing image generation models.
    """
    queryset = ImageGenModel.objects.all()
    serializer_class = ImageGenModelSerializer
    lookup_field = 'slug'

    def perform_create(self, serializer):
        logger.info("📝 Creating ImageGen model: %s", serializer.validated_data.get('name'))
        serializer.save()

    def perform_update(self, serializer):
        logger.info("✏️ Updating ImageGen model: %s", serializer.validated_data.get('name'))
        serializer.save()


@api_view(['POST'])
@permission_classes([AllowAny])
def image_gen_infer(request):
    logger.info("🚀 Received image generation inference request: %s", request.data)
    slug = request.data.get('service_slug')
    service = get_object_or_404(ImageGenModel, slug=slug)
    payload = request.data.copy()
    payload.pop('service_slug', None)
    input_image = payload.pop('image', None)
    inference_request = InferenceRequest.objects.create(
        inference_type='image_generation',
        payload=payload,
        image_gen_service=service,
        status='requested',
        input_image=input_image,
    )
    logger.info("📝 Created InferenceRequest ID %s for service '%s'", inference_request.id, slug)
    # Trigger Celery task
    process_image_gen_inference_request.delay(inference_request.id)
    logger.info("📤 Dispatched Celery task for InferenceRequest ID %s", inference_request.id)
    return Response({
        'request_id': inference_request.id,
        'status': inference_request.status
    }, status=status.HTTP_201_CREATED)


@api_view(['GET'])
@permission_classes([AllowAny])
def list_inference_requests_for_service(request, slug):
    logger.info("📥 Listing inference requests for service slug: %s", slug)
    service = get_object_or_404(ImageGenModel, slug=slug)
    requests = InferenceRequest.objects.filter(image_gen_service=service).order_by('-created_at')
    serializer = InferenceRequestSerializer(requests, many=True)
    logger.info("📦 Found %d requests for service '%s'", len(requests), slug)
    return Response(serializer.data)


class TTSServiceViewSet(viewsets.ModelViewSet):
    queryset = TTSService.objects.all()
    serializer_class = TTSServiceSerializer


@api_view(['POST'])
@permission_classes([AllowAny])
def tts_infer(request):
    """
    Accepts a TTS inference request (multipart/form-data), creates an InferenceRequest, and triggers the Celery task.
    """
    try:
        tts_service_id = request.data.get('tts_service')
        tts_service = TTSService.objects.get(id=tts_service_id)
        text_input = request.data.get('text_input', '')
        max_new_tokens = int(request.data.get('max_new_tokens', 860))
        cfg_scale = float(request.data.get('cfg_scale', 1))
        temperature = float(request.data.get('temperature', 1))
        top_p = float(request.data.get('top_p', 0.8))
        cfg_filter_top_k = int(request.data.get('cfg_filter_top_k', 15))
        speed_factor = float(request.data.get('speed_factor', 0.8))
        audio_prompt_input = request.FILES.get('audio_prompt_input')

        payload = {
            'text_input': text_input,
            'max_new_tokens': max_new_tokens,
            'cfg_scale': cfg_scale,
            'temperature': temperature,
            'top_p': top_p,
            'cfg_filter_top_k': cfg_filter_top_k,
            'speed_factor': speed_factor
        }

        inference_request = InferenceRequest.objects.create(
            inference_type='tts',
            payload=payload,
            tts_service=tts_service,
            status='requested',
        )
        if audio_prompt_input:
            inference_request.speech_input.save(audio_prompt_input.name, audio_prompt_input, save=True)

        logger.info(f"📝 Created TTS InferenceRequest ID {inference_request.id} for service '{tts_service.slug}'")
        process_tts_inference_request.delay(inference_request.id)
        logger.info(f"📤 Dispatched Celery task for TTS InferenceRequest ID {inference_request.id}")
        return Response({
            'request_id': inference_request.id,
            'status': inference_request.status
        }, status=status.HTTP_201_CREATED)
    except Exception as e:
        logger.error(f"❌ Error in tts_infer: {str(e)}")
        return Response({'error': str(e)}, status=status.HTTP_400_BAD_REQUEST)


@api_view(['GET'])
@permission_classes([AllowAny])
def list_llm_models(request):
    """
    List all active LLM models.
    """
    models = LLMModel.objects.filter(is_active=True)
    serializer = LLMModelSerializer(models, many=True)
    return Response(serializer.data)


class VideoGenServiceViewSet(viewsets.ModelViewSet):
    queryset = VideoGenService.objects.all()
    serializer_class = VideoGenServiceSerializer
    permission_classes = [AllowAny]
    lookup_field = 'slug'


@api_view(['POST'])
@permission_classes([AllowAny])
def video_gen_infer(request):
    """
    Accepts a video generation inference request (multipart/form-data), creates an InferenceRequest, and triggers the Celery task.
    """
    try:
        logger.info("🚀 Received video generation request with data: %s", request.data)
        logger.info("📁 Files in request: %s", request.FILES)

        video_gen_service_id = request.data.get('video_gen_service')
        video_gen_service = VideoGenService.objects.get(id=video_gen_service_id)
        prompt = request.data.get('prompt', '')
        negative_prompt = request.data.get('negative_prompt', '')
        num_frames = int(request.data.get('num_frames', 16))
        width = int(request.data.get('width', 512))
        height = int(request.data.get('height', 512))
        num_inference_steps = int(request.data.get('num_inference_steps', 50))
        guidance_scale = float(request.data.get('guidance_scale', 7.5))
        seed = int(request.data.get('seed', -1))
        input_image = request.FILES.get('input_image')

        logger.info("🖼️ Input image file: %s", input_image)

        payload = {
            'prompt': prompt,
            'negative_prompt': negative_prompt,
            'num_frames': num_frames,
            'width': width,
            'height': height,
            'num_inference_steps': num_inference_steps,
            'guidance_scale': guidance_scale,
            'seed': seed
        }

        inference_request = InferenceRequest.objects.create(
            inference_type='video_generation',
            payload=payload,
            video_gen_service=video_gen_service,
            status='requested',
        )

        # Save the uploaded image file if one was provided
        if input_image:
            logger.info("💾 Saving input image file to inference request")
            inference_request.input_image_file = input_image
            inference_request.save()
            logger.info("✅ Input image file saved successfully")

        logger.info(f"📝 Created Video Generation InferenceRequest ID {inference_request.id} for service '{video_gen_service.slug}'")
        process_video_gen_inference_request.delay(inference_request.id)
        logger.info(f"📤 Dispatched Celery task for Video Generation InferenceRequest ID {inference_request.id}")
        return Response({
            'request_id': inference_request.id,
            'status': inference_request.status
        }, status=status.HTTP_201_CREATED)
    except Exception as e:
        logger.error(f"❌ Error in video_gen_infer: {str(e)}")
        return Response({'error': str(e)}, status=status.HTTP_400_BAD_REQUEST)


@api_view(['GET'])
@permission_classes([AllowAny])
def list_video_gen_requests(request, slug):
    """
    List all inference requests for a specific video generation service.
    """
    try:
        service = VideoGenService.objects.get(slug=slug)
        requests = InferenceRequest.objects.filter(
            video_gen_service=service,
            inference_type='video_generation'
        ).order_by('-created_at')
        serializer = InferenceRequestSerializer(requests, many=True)
        return Response(serializer.data)
    except VideoGenService.DoesNotExist:
        return Response({'error': 'Service not found'}, status=status.HTTP_404_NOT_FOUND)
    except Exception as e:
        logger.error(f"❌ Error listing video generation requests: {str(e)}")
        return Response({'error': str(e)}, status=status.HTTP_400_BAD_REQUEST)
