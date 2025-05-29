from django.urls import path, include
from rest_framework.routers import DefaultRouter
from .views import LLMModelViewSet, ImageGenModelViewSet, image_gen_infer, list_inference_requests_for_service

router = DefaultRouter()
router.register(r'llm-models', LLMModelViewSet)
router.register(r'image-gen-models', ImageGenModelViewSet)

urlpatterns = [
    path('', include(router.urls)),
    path('image-gen-infer/', image_gen_infer, name='image-gen-infer'),
    path('image-gen-models/<slug:slug>/requests/', list_inference_requests_for_service, name='image-gen-model-requests'),
]