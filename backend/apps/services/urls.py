from django.urls import path, include
from rest_framework.routers import DefaultRouter
from .views import (
    LLMModelViewSet,
    ImageGenModelViewSet,
    image_gen_infer,
    list_inference_requests_for_service,
    TTSServiceViewSet,
    tts_infer,
    list_llm_models,
    VideoGenServiceViewSet,
    video_gen_infer,
    list_video_gen_requests
)

router = DefaultRouter()
router.register(r'llm-models', LLMModelViewSet)
router.register(r'image-gen-models', ImageGenModelViewSet)
router.register(r'tts-services', TTSServiceViewSet, basename='ttsservice')
router.register(r'video-gen-services', VideoGenServiceViewSet)

urlpatterns = [
    path('', include(router.urls)),
    path('image-gen-infer/', image_gen_infer, name='image-gen-infer'),
    path('image-gen-models/<slug:slug>/requests/', list_inference_requests_for_service, name='image-gen-model-requests'),
    path('tts-infer/', tts_infer, name='tts-infer'),
    path('video-gen-infer/', video_gen_infer, name='video-gen-infer'),
    path('video-gen-services/<slug:slug>/requests/', list_video_gen_requests, name='video-gen-requests'),
    path('llm-models/list/', list_llm_models, name='list_llm_models'),
]