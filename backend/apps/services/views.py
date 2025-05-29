from django.shortcuts import render, get_object_or_404
from rest_framework import viewsets, status
from rest_framework.decorators import api_view, permission_classes
from rest_framework.permissions import AllowAny
from rest_framework.response import Response
from .models import LLMModel, ImageGenModel
from .serializers import LLMModelSerializer, ImageGenModelSerializer
from apps.inference.models import InferenceRequest
from apps.inference.serializers import InferenceRequestSerializer
from apps.inference.tasks import process_image_gen_inference_request
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
    inference_request = InferenceRequest.objects.create(
        inference_type='image_generation',
        payload=payload,
        image_gen_service=service,
        status='requested',
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
