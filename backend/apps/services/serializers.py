from rest_framework import serializers
from .models import LLMModel, ImageGenModel, TTSService, VideoGenService

class LLMModelSerializer(serializers.ModelSerializer):
    class Meta:
        model = LLMModel
        fields = ['id', 'name', 'slug', 'base_url', 'is_active', 'created_at', 'updated_at']
        read_only_fields = ['id', 'slug', 'created_at', 'updated_at']


class ImageGenModelSerializer(serializers.ModelSerializer):
    service_type_display = serializers.CharField(source='get_service_type_display', read_only=True)

    class Meta:
        model = ImageGenModel
        fields = ['id', 'name', 'slug', 'base_url', 'service_type', 'service_type_display', 'is_active', 'created_at', 'updated_at']
        read_only_fields = ['id', 'created_at', 'updated_at']


class TTSServiceSerializer(serializers.ModelSerializer):
    class Meta:
        model = TTSService
        fields = ['id', 'slug', 'url', 'type']


class VideoGenServiceSerializer(serializers.ModelSerializer):
    class Meta:
        model = VideoGenService
        fields = ['id', 'name', 'url', 'slug', 'type']