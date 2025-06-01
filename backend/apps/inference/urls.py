from django.urls import path, include
from rest_framework.routers import DefaultRouter
from .views import InferenceRequestViewSet
from . import views

router = DefaultRouter()
router.register(r"inference", InferenceRequestViewSet)

urlpatterns = [
    path("", include(router.urls)),
    path('llms/v1/', views.llm_inference, name='llm_inference'),
]
