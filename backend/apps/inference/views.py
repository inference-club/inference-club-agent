from django.shortcuts import render
from rest_framework import viewsets, status
from rest_framework.response import Response
from .models import InferenceRequest
from .serializers import InferenceRequestSerializer
from .tasks import process_inference_request

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
